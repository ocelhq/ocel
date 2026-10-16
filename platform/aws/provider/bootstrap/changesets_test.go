package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

func TestCoreRefusesReplacementWhateverTheCallerAccepts(t *testing.T) {
	for _, accept := range []bool{false, true} {
		t.Run(fmt.Sprintf("acceptReplacements=%t", accept), func(t *testing.T) {
			stacks, apis := installedBootstrap(t)
			stacks.fallBehind(coreStackName)
			stacks.plan(coreStackName, change(cfntypes.ChangeActionModify, "StateTable", "AWS::DynamoDB::Table", cfntypes.ReplacementTrue))
			before := stacks.updates

			req := everything()
			req.AcceptReplacements = accept
			err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, req, nil)
			if err == nil {
				t.Fatal("a bootstrap that would replace the core's state table was allowed through")
			}
			if !strings.Contains(err.Error(), "StateTable") {
				t.Errorf("refusal = %q, want it to name StateTable", err)
			}
			if strings.Contains(err.Error(), "--yes") {
				t.Errorf("refusal = %q, want no flag offered: the core is never replaced", err)
			}
			if stacks.updates != before {
				t.Error("the core was written even though the plan replaced what stores every app's state")
			}
			if left := stacks.leftBehind(); len(left) != 0 {
				t.Errorf("change sets %v were neither applied nor deleted", left)
			}
		})
	}
}

func TestTagOnlyDeltaIsStillWritten(t *testing.T) {
	t.Run("a new writer writes the tag through a bootstrap that has not otherwise moved", func(t *testing.T) {
		stacks, apis := installedBootstrap(t)
		before := stacks.updates

		req := everything()
		req.Writer = "9.9.9"
		if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, req, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := stacks.stampOf(coreStackName).WrittenBy; got != "9.9.9" {
			t.Errorf("written by %q, want the writer this run names", got)
		}
		if stacks.updates != before {
			t.Errorf("a tag-only delta went through %d change sets; CloudFormation reports one as empty", stacks.updates-before)
		}
		if stacks.restamps == 0 {
			t.Error("nothing reconciled the tags an empty change set left unwritten")
		}
	})

	t.Run("a bootstrap whose tags are already current is left alone", func(t *testing.T) {
		stacks, apis := installedBootstrap(t)
		before := stacks.restamps

		if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, everything(), nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if stacks.restamps != before {
			t.Errorf("a re-run restamped %d stacks, want none", stacks.restamps-before)
		}
	})
}

func TestADevRebuildLeavesAStackItDidNotChangeAlone(t *testing.T) {
	stacks, apis := installedBootstrap(t)
	built := everything()
	built.Writer = "1.4.0"
	if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, built, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, sha := range []string{"dev+1111111", "dev+2222222"} {
		rebuilt := everything()
		rebuilt.Writer = provider.WrittenBy(sha)
		if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, rebuilt, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
	before := stacks.restamps

	rebuilt := everything()
	rebuilt.Writer = "dev+3333333"
	if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, rebuilt, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stacks.restamps != before {
		t.Errorf("a rebuild restamped %d stacks; a development build's git sha is not a reason to write CloudFormation", stacks.restamps-before)
	}
	if got := stacks.stampOf(coreStackName).WrittenBy; got != "dev+1111111" {
		t.Errorf("written by %q, want the sha that last actually wrote the stack", got)
	}
}

func TestRestampingTurnsOnlyOnTheStacksCurrentTags(t *testing.T) {
	const digest = "beef"
	for _, tc := range []struct {
		name      string
		current   Stamp
		unwritten bool
		incoming  Stamp
		writes    bool
	}{
		{
			name:     "one development build to the next",
			current:  Stamp{Digest: digest, WrittenBy: "dev+1111111"},
			incoming: Stamp{Digest: digest, WrittenBy: "dev+2222222"},
		},
		{
			name:     "a development build gives way to a release",
			current:  Stamp{Digest: digest, WrittenBy: "dev+1111111"},
			incoming: Stamp{Digest: digest, WrittenBy: "1.4.0"},
			writes:   true,
		},
		{
			name:     "a release gives way to a development build",
			current:  Stamp{Digest: digest, WrittenBy: "1.4.0"},
			incoming: Stamp{Digest: digest, WrittenBy: "dev+1111111"},
			writes:   true,
		},
		{
			name:     "the template moved under two development builds",
			current:  Stamp{Digest: digest, WrittenBy: "dev+1111111"},
			incoming: Stamp{Digest: "cafe", WrittenBy: "dev+2222222"},
			writes:   true,
		},
		{
			name:      "the stack has no writer tag at all",
			current:   Stamp{Digest: digest},
			unwritten: true,
			incoming:  Stamp{Digest: digest, WrittenBy: "dev+2222222"},
			writes:    true,
		},
		{
			name:     "the stack was written by an unknown writer",
			current:  Stamp{Digest: digest, WrittenBy: provider.WrittenBy("").String()},
			incoming: Stamp{Digest: digest, WrittenBy: "dev+2222222"},
			writes:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stacks, _ := installedBootstrap(t)
			tags := stampTags(defaultNamespace, environment.TierProduction, tc.current)
			if tc.unwritten {
				tags = cfn.WithoutTag(tags, cfn.TagBootstrappedBy)
			}
			stacks.stamped(coreStackName, tags)
			before := stacks.restamps

			if err := cfn.Restamp(context.Background(), stacks, coreStackName, nil, nil, stampTags(defaultNamespace, environment.TierProduction, tc.incoming)); err != nil {
				t.Fatalf("cfn.Restamp: %v", err)
			}
			if wrote := stacks.restamps > before; wrote != tc.writes {
				t.Errorf("restamped = %t, want %t", wrote, tc.writes)
			}
		})
	}
}

func TestChangeSetsAreDiscardedWhateverEndsTheRun(t *testing.T) {
	staleBody := "AWSTemplateFormatVersion: '2010-09-09'\nResources: {}\nOutputs: {}\n"
	staleTags := stampTags(defaultNamespace, environment.TierProduction, Stamp{Digest: "beef", WrittenBy: "1.4.0"})

	t.Run("a caller context that is already gone still takes the change set down", func(t *testing.T) {
		stacks, _ := installedBootstrap(t)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := cfn.Update(ctx, stacks, defaultNamespace.ChangeSetNameFor, keyStack(environment.TierProduction), staleBody, nil, nil, staleTags,
			func(string, []cfntypes.ResourceChange) error {
				cancel()
				return errors.New("this run stops here")
			})
		if err == nil {
			t.Fatal("cfn.Update returned nil though the review refused")
		}
		if left := stacks.leftBehind(); len(left) != 0 {
			t.Errorf("change sets %v outlived a cancelled run", left)
		}
	})

	t.Run("a panic between planning and executing still takes the change set down", func(t *testing.T) {
		stacks, _ := installedBootstrap(t)

		func() {
			defer func() { _ = recover() }()
			_ = cfn.Update(context.Background(), stacks, defaultNamespace.ChangeSetNameFor, keyStack(environment.TierProduction), staleBody, nil, nil, staleTags,
				func(string, []cfntypes.ResourceChange) error { panic("the run came apart") })
		}()
		if left := stacks.leftBehind(); len(left) != 0 {
			t.Errorf("change sets %v outlived a panicking run", left)
		}
	})
}

type deletedChangeSets struct{ *fakeCFN }

func (deletedChangeSets) DescribeChangeSet(context.Context, *cloudformation.DescribeChangeSetInput, ...func(*cloudformation.Options)) (*cloudformation.DescribeChangeSetOutput, error) {
	return &cloudformation.DescribeChangeSetOutput{Status: cfntypes.ChangeSetStatusDeleteComplete}, nil
}

func TestAChangeSetSomethingElseDeletedStopsAtOnce(t *testing.T) {
	waits := recordWaits(t)

	_, _, err := cfn.AwaitChangeSet(context.Background(), deletedChangeSets{newFakeCFN()}, "ocel-bootstrap", "ocel-1")
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

func TestAChangeSetIsNamedAfterTheStackItPlansAgainst(t *testing.T) {
	stacks, apis := installedBootstrap(t)
	stacks.fallBehind(keyStack(environment.TierProduction))

	if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, everything(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	planned := stacks.changeSetsPlanned()
	if len(planned) == 0 {
		t.Fatal("a bootstrap over stacks that had fallen behind planned no change set")
	}
	for _, id := range planned {
		stack, name, _ := strings.Cut(id, "/")
		if !strings.HasPrefix(name, stack+"-") {
			t.Errorf("the change set planning %s is named %q, and a credential is scoped to the change sets of the stacks it may write by that name", stack, name)
		}
	}
}

func change(action cfntypes.ChangeAction, logicalID, resourceType string, replacement cfntypes.Replacement) cfntypes.ResourceChange {
	return cfntypes.ResourceChange{
		Action:            action,
		LogicalResourceId: aws.String(logicalID),
		ResourceType:      aws.String(resourceType),
		Replacement:       replacement,
	}
}

type repairLog struct{ fake.Log }

func (l *repairLog) says(line string) bool {
	return slices.ContainsFunc(l.Lines(), func(said string) bool { return strings.Contains(said, line) })
}

func installedBootstrap(t *testing.T) (*fakeCFN, APIs) {
	t.Helper()
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	frontedBy(t, &fakeEdge{kind: "cloudflare"})
	apis := apisOf(stacks, ssmc, iamc, preloadedStore())
	if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, everything(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return stacks, apis
}

func TestABootstrapRunLeavesAnUnmovedStackAloneAndStopsOnAReplacementUnlessAccepted(t *testing.T) {
	t.Run("a bootstrap that has not moved plans nothing and leaves nothing behind", func(t *testing.T) {
		stacks, apis := installedBootstrap(t)
		before := stacks.updates

		if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, everything(), nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if stacks.updates != before {
			t.Errorf("a re-run applied %d change sets, want none: an unchanged bootstrap is left alone", stacks.updates-before)
		}
		if left := stacks.leftBehind(); len(left) != 0 {
			t.Errorf("change sets %v were neither applied nor deleted", left)
		}
	})

	t.Run("a replacement stops a bootstrap that was not told to accept one", func(t *testing.T) {
		stacks, apis := installedBootstrap(t)
		stack := keyStack(environment.TierProduction)
		stacks.fallBehind(stack)
		stacks.plan(stack, change(cfntypes.ChangeActionModify, "VariablesKey", "AWS::KMS::Key", cfntypes.ReplacementTrue))

		err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, everything(), nil)
		if err == nil {
			t.Fatal("a bootstrap that would replace a live key was allowed through")
		}
		if !strings.Contains(err.Error(), "VariablesKey") {
			t.Errorf("refusal = %q, want it to name VariablesKey", err)
		}
		if left := stacks.leftBehind(); len(left) != 0 {
			t.Errorf("change sets %v were neither applied nor deleted", left)
		}
	})

	t.Run("accepting replacements writes the stack", func(t *testing.T) {
		stacks, apis := installedBootstrap(t)
		stack := keyStack(environment.TierProduction)
		stacks.fallBehind(stack)
		stacks.plan(stack, change(cfntypes.ChangeActionModify, "VariablesKey", "AWS::KMS::Key", cfntypes.ReplacementTrue))

		req := everything()
		req.AcceptReplacements = true
		if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, req, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if stacks.template(stack) == behindTemplate {
			t.Error("the stack was left at the older template even though replacements were accepted")
		}
	})
}
