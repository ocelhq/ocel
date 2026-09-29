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

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

var repairPrincipals = map[string]bool{
	"AWS::IAM::User":       true,
	"AWS::IAM::AccessKey":  true,
	"AWS::IAM::Group":      true,
	"AWS::IAM::UserPolicy": true,
}

var repairAllowed = map[string]bool{
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

func isCoreStack(ns Namespace, stackName string) bool {
	core, _ := ns.StackNameFor(environment.TierProduction)
	preview, _ := ns.StackNameFor(environment.TierPreview)
	return stackName == core || stackName == preview
}

func restampOnly(c cfntypes.ResourceChange) bool {
	return c.Action == cfntypes.ChangeActionModify &&
		len(c.Scope) == 1 && c.Scope[0] == cfntypes.ResourceAttributeTags
}

func repairable(ns Namespace) cfn.ChangeReview {
	return func(stackName string, changes []cfntypes.ResourceChange) error {
		return repairableChange(ns, stackName, changes)
	}
}

func repairableChange(ns Namespace, stackName string, changes []cfntypes.ResourceChange) error {
	if isCoreStack(ns, stackName) {
		return fmt.Errorf("%s is the bootstrap's core, and the core is only ever written by an explicit bootstrap", stackName)
	}
	for _, c := range changes {
		id := aws.ToString(c.LogicalResourceId)
		kind := aws.ToString(c.ResourceType)
		switch {
		case restampOnly(c):
			continue
		case repairPrincipals[kind]:
			return fmt.Errorf("%s in %s is a %s, and a principal is only ever written by an explicit bootstrap", id, stackName, kind)
		case c.Replacement == cfntypes.ReplacementTrue || c.Replacement == cfntypes.ReplacementConditional:
			return fmt.Errorf("%s in %s (%s) would be replaced, not updated in place", id, stackName, kind)
		case c.Action == cfntypes.ChangeActionRemove:
			return fmt.Errorf("%s in %s (%s) would be removed", id, stackName, kind)
		case c.Action != cfntypes.ChangeActionAdd && c.Action != cfntypes.ChangeActionModify:
			return fmt.Errorf("%s in %s (%s) would be %sed, which only an explicit bootstrap does", id, stackName, kind, c.Action)
		case !repairAllowed[kind]:
			return fmt.Errorf("%s in %s is a %s, which only an explicit bootstrap writes", id, stackName, kind)
		}
	}
	return nil
}

func AdmitReplacements(ns Namespace, accept bool, progress progress.Log) cfn.ChangeReview {
	progress = ensureProgress(progress)
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
		progress.Warn(fmt.Sprintf("Stack %s would replace %s rather than update it in place", stackName, strings.Join(replaced, ", ")))
		if isCoreStack(ns, stackName) {
			return fmt.Errorf(
				"writing %s would replace %s rather than update it in place, and every Pulumi state this account stores lives in it: every app deployed from this bootstrap would be orphaned.\nNo flag writes it anyway. Upgrade to a CLI whose core is an in-place update of this one",
				stackName, strings.Join(replaced, ", "),
			)
		}
		if accept {
			return nil
		}
		return fmt.Errorf(
			"writing %s would replace %s rather than update it in place, and what it contains does not survive that.\nRe-run with --yes to write it anyway",
			stackName, strings.Join(replaced, ", "),
		)
	}
}

type RepairRequest struct {
	Features []string
	Writer   provider.WrittenBy
}

var ErrRepairNotPermitted = errors.New("these credentials may not write this account's bootstrap stacks")

func RefusedWrite(err error) bool {
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

func Repair(ctx context.Context, apis APIs, ns Namespace, tier environment.Tier, req RepairRequest, progress progress.Log) (bool, error) {
	target, err := specFor(ns, tier)
	if err != nil {
		return false, err
	}
	return repair(ctx, apis, target, req, ensureProgress(progress))
}

func repair(ctx context.Context, apis APIs, target spec, req RepairRequest, progress progress.Log) (bool, error) {
	deployed, refs, err := readBootstrap(ctx, apis.CFN, target.ns, target.tier)
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

	repaired := false
	for _, level := range levels {
		for _, name := range level {
			i := slices.IndexFunc(stale, func(s StackStamp) bool { return s.Feature == name })
			if i < 0 {
				continue
			}
			done, err := repairStack(ctx, apis, target.ns, target.tier, stale[i], deployed, refs, req.Writer, progress)
			if err != nil {
				if RefusedWrite(err) {
					return repaired, ErrRepairNotPermitted
				}
				progress.Warn(fmt.Sprintf("Could not refresh stack %s, so this deploy runs against it unchanged: %v", stale[i].Name, err))
				continue
			}
			repaired = repaired || done
		}
	}
	return repaired, nil
}

func repairStack(ctx context.Context, apis APIs, ns Namespace, tier environment.Tier, stale StackStamp, deployed Deployed, refs stackRefs, writer provider.WrittenBy, progress progress.Log) (bool, error) {
	f, ok := featureNamed(stale.Feature)
	if !ok {
		return false, fmt.Errorf("this provider has no feature named %q", stale.Feature)
	}

	current, err := cfn.DescribeStack(ctx, apis.CFN, stale.Name)
	if err != nil || current == nil {
		return false, err
	}
	if stackBusy(current.StackStatus) {
		return false, waitOutRun(ctx, apis.CFN, stale, progress)
	}

	stack, err := f.staged(ctx, apis.Store, featureInputs{
		ns:             ns,
		tier:           tier,
		artifactBucket: deployed.ArtifactBucket,
		refs:           refs,
		alongside:      deployed.Features,
		varsKey:        broughtVarsKey(deployed.Outputs),
	})
	if err != nil {
		return false, err
	}
	tags := stampTags(ns, Stamp{Schema: provider.BootstrapSchema, Digest: cfn.TemplateDigest(stack.body), WrittenBy: writer.String()})
	capabilities := []cfntypes.Capability{cfntypes.CapabilityCapabilityNamedIam}
	if err := cfn.Update(ctx, apis.CFN, ns.ChangeSetNameFor, stale.Name, stack.body, stack.params, capabilities, tags, repairable(ns)); err != nil {
		return false, err
	}
	progress.Say("Refreshed stack " + stale.Name)
	return true, nil
}

func waitOutRun(ctx context.Context, stacks cfn.API, stale StackStamp, progress progress.Log) error {
	idle, err := awaitStackIdle(ctx, stacks, stale.Name, progress)
	if err != nil {
		return err
	}
	if !idle {
		progress.Warn(fmt.Sprintf("Stack %s is still being written by another run, so this deploy runs against it unchanged", stale.Name))
		return nil
	}
	stack, err := cfn.DescribeStack(ctx, stacks, stale.Name)
	if err != nil {
		return err
	}
	if stack != nil && readStamp(stack.Tags).Digest == stale.Intended {
		return nil
	}
	progress.Warn(fmt.Sprintf("Stack %s was written by another run and is still behind the template this build renders, so this deploy leaves it to whoever is writing it", stale.Name))
	return nil
}

const idleAttempts = 6

func awaitStackIdle(ctx context.Context, stacks cfn.API, stackName string, progress progress.Log) (bool, error) {
	for attempt := 0; ; attempt++ {
		stack, err := cfn.DescribeStack(ctx, stacks, stackName)
		if err != nil {
			return false, err
		}
		if stack == nil || !stackBusy(stack.StackStatus) {
			return true, nil
		}
		if attempt+1 >= idleAttempts {
			return false, nil
		}
		progress.Say(fmt.Sprintf("Stack %s is %s under another run; look %d of %d before this deploy stops waiting on it", stackName, stack.StackStatus, attempt+1, idleAttempts))
		if err := cfn.WaitBefore(ctx, cfn.ChangeSetDelay(attempt)); err != nil {
			return false, err
		}
	}
}

func stackBusy(status cfntypes.StackStatus) bool {
	return strings.HasSuffix(string(status), "_IN_PROGRESS")
}
