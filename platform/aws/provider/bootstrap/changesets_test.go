package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	smithy "github.com/aws/smithy-go"
)

func TestCoreRefusesReplacementWhateverTheCallerAccepts(t *testing.T) {
	for _, accept := range []bool{false, true} {
		t.Run(fmt.Sprintf("acceptReplacements=%t", accept), func(t *testing.T) {
			cfn, apis := standingBootstrap(t)
			cfn.fallBehind(StackName)
			cfn.plan(StackName, change(cfntypes.ChangeActionModify, "StateTable", "AWS::DynamoDB::Table", cfntypes.ReplacementTrue))
			before := cfn.updates

			req := everything()
			req.AcceptReplacements = accept
			err := Run(context.Background(), apis, req, nil, nil)
			if err == nil {
				t.Fatal("a bootstrap that would replace the core's state table was allowed through")
			}
			if !strings.Contains(err.Error(), "StateTable") {
				t.Errorf("refusal = %q, want it to name StateTable", err)
			}
			if strings.Contains(err.Error(), "--yes") {
				t.Errorf("refusal = %q, want no flag offered: the core is never replaced", err)
			}
			if cfn.updates != before {
				t.Error("the core was written even though the plan replaced what holds every app's state")
			}
			if left := cfn.leftBehind(); len(left) != 0 {
				t.Errorf("change sets %v were neither applied nor deleted", left)
			}
		})
	}
}

func TestTagOnlyDeltaIsStillWritten(t *testing.T) {
	t.Run("opting into auto-heal writes the tag through a bootstrap that has not otherwise moved", func(t *testing.T) {
		cfn, apis := standingBootstrap(t)
		before := cfn.updates

		on := true
		req := everything()
		req.AutoHeal = &on
		if err := Run(context.Background(), apis, req, nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !cfn.stampOf(StackName).AutoHeal {
			t.Error("the account opted into auto-heal and the core still reads as opted out")
		}
		if cfn.updates != before {
			t.Errorf("a tag-only delta went through %d change sets; CloudFormation reports one as empty", cfn.updates-before)
		}
		if cfn.restamps == 0 {
			t.Error("nothing reconciled the tags an empty change set left unwritten")
		}
	})

	t.Run("a bootstrap whose tags already stand is left alone", func(t *testing.T) {
		cfn, apis := standingBootstrap(t)
		before := cfn.restamps

		if err := Run(context.Background(), apis, everything(), nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if cfn.restamps != before {
			t.Errorf("a re-run restamped %d stacks, want none", cfn.restamps-before)
		}
	})
}

func TestChangeSetsAreDiscardedWhateverEndsTheRun(t *testing.T) {
	staleBody := "AWSTemplateFormatVersion: '2010-09-09'\nResources: {}\nOutputs: {}\n"
	staleTags := stampTags(Stamp{Schema: RequiredSchema, Digest: "beef", WrittenBy: "1.4.0"})

	t.Run("a caller context that is already gone still takes the change set down", func(t *testing.T) {
		cfn, _ := standingBootstrap(t)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := updateCFNStack(ctx, cfn, isrStack(ClassProduction), staleBody, nil, nil, staleTags,
			func(string, []cfntypes.ResourceChange) error {
				cancel()
				return errors.New("this run stops here")
			})
		if err == nil {
			t.Fatal("updateCFNStack returned nil though the review refused")
		}
		if left := cfn.leftBehind(); len(left) != 0 {
			t.Errorf("change sets %v outlived a cancelled run", left)
		}
	})

	t.Run("a panic between planning and executing still takes the change set down", func(t *testing.T) {
		cfn, _ := standingBootstrap(t)

		func() {
			defer func() { _ = recover() }()
			_ = updateCFNStack(context.Background(), cfn, isrStack(ClassProduction), staleBody, nil, nil, staleTags,
				func(string, []cfntypes.ResourceChange) error { panic("the run came apart") })
		}()
		if left := cfn.leftBehind(); len(left) != 0 {
			t.Errorf("change sets %v outlived a panicking run", left)
		}
	})
}

type deletedChangeSets struct{ *fakeCFN }

func (deletedChangeSets) DescribeChangeSet(context.Context, *cloudformation.DescribeChangeSetInput, ...func(*cloudformation.Options)) (*cloudformation.DescribeChangeSetOutput, error) {
	return &cloudformation.DescribeChangeSetOutput{Status: cfntypes.ChangeSetStatusDeleteComplete}, nil
}

func TestAChangeSetSomethingElseDeletedStopsAtOnce(t *testing.T) {
	waits := holdNothing(t)

	_, _, err := awaitChangeSet(context.Background(), deletedChangeSets{newFakeCFN()}, "ocel-1")
	if err == nil {
		t.Fatal("a deleted change set was waited on as though it were still being built")
	}
	if !strings.Contains(err.Error(), string(cfntypes.ChangeSetStatusDeleteComplete)) {
		t.Errorf("error = %q, want it to name the status that ended it", err)
	}
	if len(*waits) != 0 {
		t.Errorf("waited %d times on a change set that was already gone", len(*waits))
	}
}

type deniedChangeSets struct{ *fakeCFN }

func (deniedChangeSets) CreateChangeSet(context.Context, *cloudformation.CreateChangeSetInput, ...func(*cloudformation.Options)) (*cloudformation.CreateChangeSetOutput, error) {
	return nil, &smithy.GenericAPIError{Code: "AccessDenied", Message: "not authorized to perform: cloudformation:CreateChangeSet"}
}

func TestHealRefusedByTheseCredentialsSaysSoOnce(t *testing.T) {
	cfn, apis := standingBootstrap(t)
	cfn.fallBehind(isrStack(ClassProduction))
	apis.CFN = deniedChangeSets{cfn}
	var log healLog

	healed, err := Heal(context.Background(), apis, HealRequest{Features: featureNames(), Writer: "1.4.0"}, log.write)
	if !errors.Is(err, ErrHealNotPermitted) {
		t.Fatalf("Heal err = %v, want ErrHealNotPermitted", err)
	}
	if healed {
		t.Error("a heal that was refused outright reported that it wrote something")
	}
	if log.says("could not refresh") {
		t.Errorf("heal said %v, want no alarming per-stack failure for a credential that simply may not write", log.lines)
	}
}

func TestSettlingIsBoundedAndReported(t *testing.T) {
	waits := holdNothing(t)
	cfn, apis := standingBootstrap(t)
	stack := isrStack(ClassProduction)
	cfn.fallBehind(stack)
	cfn.busy(stack, settleAttempts*4)
	var log healLog

	if _, err := Heal(context.Background(), apis, HealRequest{Features: featureNames(), Writer: "1.4.0"}, log.write); err != nil {
		t.Fatalf("Heal: %v", err)
	}
	if len(*waits) >= changeSetAttempts {
		t.Errorf("waited %d times on a stack another run holds; that budget belongs to building a change set, not to standing by", len(*waits))
	}
	if !log.says("before this deploy stops waiting") {
		t.Errorf("heal said %v, want it to report progress while it stood by", log.lines)
	}
}

func TestAStackTagPropagatedOntoAPrincipalDoesNotBlockAHeal(t *testing.T) {
	cfn, apis := standingBootstrap(t)
	stack := edgeStack(ClassProduction)
	cfn.fallBehind(stack)
	cfn.plan(stack,
		cfntypes.ResourceChange{
			Action:            cfntypes.ChangeActionModify,
			LogicalResourceId: aws.String("EdgeUser"),
			ResourceType:      aws.String("AWS::IAM::User"),
			Scope:             []cfntypes.ResourceAttribute{cfntypes.ResourceAttributeTags},
		},
		change(cfntypes.ChangeActionModify, "TagPublisher", "AWS::Lambda::Function", cfntypes.ReplacementFalse),
	)
	var log healLog

	healed, err := Heal(context.Background(), apis, HealRequest{Features: featureNames(), Writer: "1.4.0"}, log.write)
	if err != nil {
		t.Fatalf("Heal: %v", err)
	}
	if !healed {
		t.Fatalf("the edge stack was refused a refresh: %v", log.lines)
	}
	if log.says("is a AWS::IAM::User") {
		t.Errorf("heal said %v, want a stack tag landing on the edge user to count for nothing", log.lines)
	}
}

func TestAPrincipalWhoseShapeChangesStillStopsAHeal(t *testing.T) {
	cfn, apis := standingBootstrap(t)
	stack := edgeStack(ClassProduction)
	cfn.fallBehind(stack)
	cfn.plan(stack, cfntypes.ResourceChange{
		Action:            cfntypes.ChangeActionModify,
		LogicalResourceId: aws.String("EdgeUser"),
		ResourceType:      aws.String("AWS::IAM::User"),
		Scope:             []cfntypes.ResourceAttribute{cfntypes.ResourceAttributeTags, cfntypes.ResourceAttributeProperties},
	})
	var log healLog

	healed, _ := Heal(context.Background(), apis, HealRequest{Features: featureNames(), Writer: "1.4.0"}, log.write)
	if healed {
		t.Error("a heal rewrote the policy of the identity the edge signs its calls with")
	}
	if !log.says("EdgeUser") {
		t.Errorf("heal said %v, want it to name what stopped it", log.lines)
	}
}
