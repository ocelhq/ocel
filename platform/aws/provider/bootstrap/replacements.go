package bootstrap

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	smithy "github.com/aws/smithy-go"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

func isCoreStack(ns Namespace, stackName string) bool {
	core, _ := ns.StackNameFor(environment.TierProduction)
	preview, _ := ns.StackNameFor(environment.TierPreview)
	return stackName == core || stackName == preview
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
