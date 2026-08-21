package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	smithy "github.com/aws/smithy-go"
)

var healPrincipals = map[string]bool{
	"AWS::IAM::User":       true,
	"AWS::IAM::AccessKey":  true,
	"AWS::IAM::Group":      true,
	"AWS::IAM::UserPolicy": true,
}

var healAllowed = map[string]bool{
	"AWS::Lambda::Function":           true,
	"AWS::Lambda::Url":                true,
	"AWS::Lambda::Permission":         true,
	"AWS::Lambda::EventSourceMapping": true,
	"AWS::IAM::Role":                  true,
	"AWS::IAM::Policy":                true,
	"AWS::IAM::RolePolicy":            true,
	"AWS::SQS::Queue":                 true,
	"AWS::SQS::QueuePolicy":           true,
	"AWS::Logs::LogGroup":             true,
}

func isCoreStack(stackName string) bool {
	return stackName == StackName || stackName == PreviewStackName
}

func restampOnly(c cfntypes.ResourceChange) bool {
	return c.Action == cfntypes.ChangeActionModify &&
		len(c.Scope) == 1 && c.Scope[0] == cfntypes.ResourceAttributeTags
}

func healable(stackName string, changes []cfntypes.ResourceChange) error {
	if isCoreStack(stackName) {
		return fmt.Errorf("%s is the substrate's core, and the core is only ever written by an explicit bootstrap", stackName)
	}
	for _, c := range changes {
		id := aws.ToString(c.LogicalResourceId)
		kind := aws.ToString(c.ResourceType)
		switch {
		case restampOnly(c):
			continue
		case healPrincipals[kind]:
			return fmt.Errorf("%s in %s is a %s, and a principal is only ever written by an explicit bootstrap", id, stackName, kind)
		case c.Replacement == cfntypes.ReplacementTrue || c.Replacement == cfntypes.ReplacementConditional:
			return fmt.Errorf("%s in %s (%s) would be replaced, not updated in place", id, stackName, kind)
		case c.Action == cfntypes.ChangeActionRemove:
			return fmt.Errorf("%s in %s (%s) would be removed", id, stackName, kind)
		case c.Action != cfntypes.ChangeActionAdd && c.Action != cfntypes.ChangeActionModify:
			return fmt.Errorf("%s in %s (%s) would be %sed, which only an explicit bootstrap does", id, stackName, kind, c.Action)
		case !healAllowed[kind]:
			return fmt.Errorf("%s in %s is a %s, which only an explicit bootstrap writes", id, stackName, kind)
		}
	}
	return nil
}

func admitReplacements(accept bool, log func(string)) changeReview {
	return func(stackName string, changes []cfntypes.ResourceChange) error {
		var replaced []string
		for _, c := range changes {
			if c.Replacement != cfntypes.ReplacementTrue && c.Replacement != cfntypes.ReplacementConditional {
				continue
			}
			replaced = append(replaced, fmt.Sprintf("%s (%s)", aws.ToString(c.LogicalResourceId), aws.ToString(c.ResourceType)))
		}
		if len(replaced) == 0 {
			return nil
		}
		if log != nil {
			log(fmt.Sprintf("%s replaces rather than updates: %s", stackName, strings.Join(replaced, ", ")))
		}
		if isCoreStack(stackName) {
			return fmt.Errorf(
				"writing %s would replace %s rather than update it in place, and every Pulumi state this account holds lives in it: every app deployed from this substrate would be orphaned.\nNo flag writes it anyway. Upgrade to a CLI whose core is an in-place update of this one",
				stackName, strings.Join(replaced, ", "),
			)
		}
		if accept {
			return nil
		}
		return fmt.Errorf(
			"writing %s would replace %s rather than update it in place, and what it holds does not survive that.\nRe-run with --yes to write it anyway",
			stackName, strings.Join(replaced, ", "),
		)
	}
}

type HealRequest struct {
	Features []string
	Writer   Writer
}

var ErrHealNotPermitted = errors.New("these credentials may not write this account's bootstrap stacks")

func healRefused(err error) bool {
	var api smithy.APIError
	if !errors.As(err, &api) {
		return false
	}
	switch api.ErrorCode() {
	case "AccessDenied", "AccessDeniedException", "UnauthorizedOperation":
		return true
	default:
		return false
	}
}

func Heal(ctx context.Context, apis APIs, req HealRequest, log func(string)) (bool, error) {
	return heal(ctx, apis, productionSubstrate(), req, log)
}

func HealPreview(ctx context.Context, apis APIs, req HealRequest, log func(string)) (bool, error) {
	return heal(ctx, apis, previewSubstrate(), req, log)
}

func heal(ctx context.Context, apis APIs, sub substrate, req HealRequest, log func(string)) (bool, error) {
	if log == nil {
		log = func(string) {}
	}
	deployed, refs, err := readSubstrate(ctx, apis.CFN, sub.class)
	if err != nil {
		return false, err
	}
	var stale []StackStamp
	for _, stack := range deployed.Stale(req.Features) {
		if stack.Feature != "" {
			stale = append(stale, stack)
		}
	}
	if len(stale) == 0 {
		return false, nil
	}

	levels, err := featureLevels(deployed.Features.Names())
	if err != nil {
		return false, err
	}

	healed := false
	for _, level := range levels {
		for _, name := range level {
			i := slices.IndexFunc(stale, func(s StackStamp) bool { return s.Feature == name })
			if i < 0 {
				continue
			}
			done, err := healStack(ctx, apis, sub.class, stale[i], deployed, refs, req.Writer, log)
			if err != nil {
				if healRefused(err) {
					return healed, ErrHealNotPermitted
				}
				log(fmt.Sprintf("could not refresh %s, and this deploy runs against it as it stands: %v", stale[i].Name, err))
				continue
			}
			healed = healed || done
		}
	}
	return healed, nil
}

func healStack(ctx context.Context, apis APIs, class string, stale StackStamp, deployed Deployed, refs stackRefs, writer Writer, log func(string)) (bool, error) {
	f, ok := featureNamed(stale.Feature)
	if !ok {
		return false, fmt.Errorf("this provider has no feature named %q", stale.Feature)
	}

	current, err := describeStack(ctx, apis.CFN, stale.Name)
	if err != nil || current == nil {
		return false, err
	}
	if stackSettling(current.StackStatus) {
		return false, waitOutRun(ctx, apis.CFN, stale, log)
	}

	code, err := f.payloads(ctx, apis.Store, deployed.ArtifactBucket)
	if err != nil {
		return false, err
	}
	stack := f.template(featureInputs{
		class:     class,
		code:      code,
		refs:      refs,
		alongside: deployed.Features,
	})
	tags := stampTags(Stamp{Schema: RequiredSchema, Digest: TemplateDigest(stack.body), WrittenBy: writer.String()})
	capabilities := []cfntypes.Capability{cfntypes.CapabilityCapabilityNamedIam}
	if err := updateCFNStack(ctx, apis.CFN, stale.Name, stack.body, stack.params, capabilities, tags, healable); err != nil {
		return false, err
	}
	log(fmt.Sprintf("refreshed %s", stale.Name))
	return true, nil
}

func waitOutRun(ctx context.Context, cfn CFNAPI, stale StackStamp, log func(string)) error {
	settled, err := settleStack(ctx, cfn, stale.Name, log)
	if err != nil {
		return err
	}
	if !settled {
		log(fmt.Sprintf("%s is still being written by another run, and this deploy runs against it as it stands", stale.Name))
		return nil
	}
	stack, err := describeStack(ctx, cfn, stale.Name)
	if err != nil {
		return err
	}
	if stack != nil && readStamp(stack.Tags).Digest == stale.Intended {
		return nil
	}
	log(fmt.Sprintf("%s was written by another run and is still behind what this build carries, so this deploy leaves it to whoever is writing it", stale.Name))
	return nil
}

const settleAttempts = 6

func settleStack(ctx context.Context, cfn CFNAPI, stackName string, log func(string)) (bool, error) {
	for attempt := 0; ; attempt++ {
		stack, err := describeStack(ctx, cfn, stackName)
		if err != nil {
			return false, err
		}
		if stack == nil || !stackSettling(stack.StackStatus) {
			return true, nil
		}
		if attempt+1 >= settleAttempts {
			return false, nil
		}
		log(fmt.Sprintf("%s is %s under another run; look %d of %d before this deploy stops waiting on it", stackName, stack.StackStatus, attempt+1, settleAttempts))
		if err := holdBefore(ctx, changeSetDelay(attempt)); err != nil {
			return false, err
		}
	}
}

func stackSettling(status cfntypes.StackStatus) bool {
	return strings.HasSuffix(string(status), "_IN_PROGRESS")
}
