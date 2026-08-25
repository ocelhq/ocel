package bootstrap

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
)

func change(action cfntypes.ChangeAction, logicalID, resourceType string, replacement cfntypes.Replacement) cfntypes.ResourceChange {
	return cfntypes.ResourceChange{
		Action:            action,
		LogicalResourceId: aws.String(logicalID),
		ResourceType:      aws.String(resourceType),
		Replacement:       replacement,
	}
}

func TestHealable(t *testing.T) {
	tests := []struct {
		name    string
		stack   string
		changes []cfntypes.ResourceChange
		names   string
	}{
		{
			name:  "nothing to change",
			stack: "ocel-bootstrap-isr",
		},
		{
			name:    "the production core never heals",
			stack:   StackName,
			changes: []cfntypes.ResourceChange{change(cfntypes.ChangeActionModify, "Revalidator", "AWS::Lambda::Function", cfntypes.ReplacementFalse)},
			names:   StackName,
		},
		{
			name:    "the preview core never heals",
			stack:   PreviewStackName,
			changes: []cfntypes.ResourceChange{change(cfntypes.ChangeActionModify, "Revalidator", "AWS::Lambda::Function", cfntypes.ReplacementFalse)},
			names:   PreviewStackName,
		},
		{
			name:  "an empty change set on the core still never heals",
			stack: StackName,
			names: StackName,
		},
		{
			name:    "adding a user",
			stack:   "ocel-bootstrap-cloudflare-edge",
			changes: []cfntypes.ResourceChange{change(cfntypes.ChangeActionAdd, "EdgeUser", "AWS::IAM::User", cfntypes.ReplacementFalse)},
			names:   "EdgeUser",
		},
		{
			name:    "modifying a user",
			stack:   "ocel-bootstrap-cloudflare-edge",
			changes: []cfntypes.ResourceChange{change(cfntypes.ChangeActionModify, "EdgeUser", "AWS::IAM::User", cfntypes.ReplacementFalse)},
			names:   "EdgeUser",
		},
		{
			name:    "removing a user",
			stack:   "ocel-bootstrap-cloudflare-edge",
			changes: []cfntypes.ResourceChange{change(cfntypes.ChangeActionRemove, "EdgeUser", "AWS::IAM::User", cfntypes.ReplacementFalse)},
			names:   "EdgeUser",
		},
		{
			name:    "adding an access key",
			stack:   "ocel-bootstrap-cloudflare-edge",
			changes: []cfntypes.ResourceChange{change(cfntypes.ChangeActionAdd, "EdgeKey", "AWS::IAM::AccessKey", cfntypes.ReplacementFalse)},
			names:   "EdgeKey",
		},
		{
			name:    "modifying an access key",
			stack:   "ocel-bootstrap-cloudflare-edge",
			changes: []cfntypes.ResourceChange{change(cfntypes.ChangeActionModify, "EdgeKey", "AWS::IAM::AccessKey", cfntypes.ReplacementFalse)},
			names:   "EdgeKey",
		},
		{
			name:    "removing an access key",
			stack:   "ocel-bootstrap-cloudflare-edge",
			changes: []cfntypes.ResourceChange{change(cfntypes.ChangeActionRemove, "EdgeKey", "AWS::IAM::AccessKey", cfntypes.ReplacementFalse)},
			names:   "EdgeKey",
		},
		{
			name:    "adding a group",
			stack:   "ocel-bootstrap-isr",
			changes: []cfntypes.ResourceChange{change(cfntypes.ChangeActionAdd, "Operators", "AWS::IAM::Group", cfntypes.ReplacementFalse)},
			names:   "Operators",
		},
		{
			name:    "modifying a group",
			stack:   "ocel-bootstrap-isr",
			changes: []cfntypes.ResourceChange{change(cfntypes.ChangeActionModify, "Operators", "AWS::IAM::Group", cfntypes.ReplacementFalse)},
			names:   "Operators",
		},
		{
			name:    "removing a group",
			stack:   "ocel-bootstrap-isr",
			changes: []cfntypes.ResourceChange{change(cfntypes.ChangeActionRemove, "Operators", "AWS::IAM::Group", cfntypes.ReplacementFalse)},
			names:   "Operators",
		},
		{
			name:    "adding a user policy",
			stack:   "ocel-bootstrap-cloudflare-edge",
			changes: []cfntypes.ResourceChange{change(cfntypes.ChangeActionAdd, "EdgeUserPolicy", "AWS::IAM::UserPolicy", cfntypes.ReplacementFalse)},
			names:   "EdgeUserPolicy",
		},
		{
			name:    "modifying a user policy",
			stack:   "ocel-bootstrap-cloudflare-edge",
			changes: []cfntypes.ResourceChange{change(cfntypes.ChangeActionModify, "EdgeUserPolicy", "AWS::IAM::UserPolicy", cfntypes.ReplacementFalse)},
			names:   "EdgeUserPolicy",
		},
		{
			name:    "removing a user policy",
			stack:   "ocel-bootstrap-cloudflare-edge",
			changes: []cfntypes.ResourceChange{change(cfntypes.ChangeActionRemove, "EdgeUserPolicy", "AWS::IAM::UserPolicy", cfntypes.ReplacementFalse)},
			names:   "EdgeUserPolicy",
		},
		{
			name:    "a replacement",
			stack:   "ocel-bootstrap-isr",
			changes: []cfntypes.ResourceChange{change(cfntypes.ChangeActionModify, "RevalidateQueue", "AWS::SQS::Queue", cfntypes.ReplacementTrue)},
			names:   "RevalidateQueue",
		},
		{
			name:    "a conditional replacement",
			stack:   "ocel-bootstrap-isr",
			changes: []cfntypes.ResourceChange{change(cfntypes.ChangeActionModify, "RevalidateQueue", "AWS::SQS::Queue", cfntypes.ReplacementConditional)},
			names:   "RevalidateQueue",
		},
		{
			name:    "a removal",
			stack:   "ocel-bootstrap-isr",
			changes: []cfntypes.ResourceChange{change(cfntypes.ChangeActionRemove, "TagInvalidator", "AWS::Lambda::Function", cfntypes.ReplacementFalse)},
			names:   "TagInvalidator",
		},
		{
			name:    "an import",
			stack:   "ocel-bootstrap-isr",
			changes: []cfntypes.ResourceChange{change(cfntypes.ChangeActionImport, "TagInvalidator", "AWS::Lambda::Function", cfntypes.ReplacementFalse)},
			names:   "TagInvalidator",
		},
		{
			name:    "a resource kind outside the allowed set",
			stack:   "ocel-bootstrap-isr",
			changes: []cfntypes.ResourceChange{change(cfntypes.ChangeActionModify, "AssetBucket", "AWS::S3::Bucket", cfntypes.ReplacementFalse)},
			names:   "AssetBucket",
		},
		{
			name:  "the offender is named even when it follows allowed changes",
			stack: "ocel-bootstrap-isr",
			changes: []cfntypes.ResourceChange{
				change(cfntypes.ChangeActionModify, "Revalidator", "AWS::Lambda::Function", cfntypes.ReplacementFalse),
				change(cfntypes.ChangeActionModify, "StateTable", "AWS::DynamoDB::Table", cfntypes.ReplacementFalse),
			},
			names: "StateTable",
		},
		{
			name:  "the whole allowed set",
			stack: "ocel-bootstrap-isr",
			changes: []cfntypes.ResourceChange{
				change(cfntypes.ChangeActionAdd, "Revalidator", "AWS::Lambda::Function", cfntypes.ReplacementFalse),
				change(cfntypes.ChangeActionModify, "TagInvalidator", "AWS::Lambda::Function", cfntypes.ReplacementFalse),
				change(cfntypes.ChangeActionAdd, "OptimizerUrl", "AWS::Lambda::Url", cfntypes.ReplacementFalse),
				change(cfntypes.ChangeActionModify, "OptimizerPermission", "AWS::Lambda::Permission", cfntypes.ReplacementFalse),
				change(cfntypes.ChangeActionAdd, "InvalidatorSource", "AWS::Lambda::EventSourceMapping", cfntypes.ReplacementFalse),
				change(cfntypes.ChangeActionModify, "RevalidatorRole", "AWS::IAM::Role", cfntypes.ReplacementFalse),
				change(cfntypes.ChangeActionAdd, "RevalidatorPolicy", "AWS::IAM::Policy", cfntypes.ReplacementFalse),
				change(cfntypes.ChangeActionModify, "RevalidatorRolePolicy", "AWS::IAM::RolePolicy", cfntypes.ReplacementFalse),
				change(cfntypes.ChangeActionAdd, "RevalidateQueue", "AWS::SQS::Queue", cfntypes.ReplacementFalse),
				change(cfntypes.ChangeActionModify, "RevalidateQueuePolicy", "AWS::SQS::QueuePolicy", cfntypes.ReplacementFalse),
				change(cfntypes.ChangeActionAdd, "RevalidatorLogs", "AWS::Logs::LogGroup", cfntypes.ReplacementFalse),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := healable(tt.stack, tt.changes)
			if tt.names == "" {
				if err != nil {
					t.Fatalf("healable() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("healable() = nil, want a refusal naming %s", tt.names)
			}
			if !strings.Contains(err.Error(), tt.names) {
				t.Fatalf("healable() = %q, want it to name %s", err, tt.names)
			}
		})
	}
}

type healLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *healLog) write(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
}

func (l *healLog) says(substr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

func standingBootstrap(t *testing.T) (*fakeCFN, APIs) {
	t.Helper()
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	frontedBy(t, &fakeEdge{kind: "cloudflare"})
	apis := apisOf(cfn, ssmc, iamc, preloadedStore())
	if err := Run(context.Background(), apis, ClassProduction, everything(), nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return cfn, apis
}

func TestChangeSets(t *testing.T) {
	t.Run("a bootstrap that has not moved plans nothing and leaves nothing behind", func(t *testing.T) {
		cfn, apis := standingBootstrap(t)
		before := cfn.updates

		if err := Run(context.Background(), apis, ClassProduction, everything(), nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if cfn.updates != before {
			t.Errorf("a re-run applied %d change sets, want none: an unchanged bootstrap is left alone", cfn.updates-before)
		}
		if left := cfn.leftBehind(); len(left) != 0 {
			t.Errorf("change sets %v were neither applied nor deleted", left)
		}
	})

	t.Run("a replacement stops a bootstrap that was not told to accept one", func(t *testing.T) {
		cfn, apis := standingBootstrap(t)
		stack := isrStack(ClassProduction)
		cfn.fallBehind(stack)
		cfn.plan(stack, change(cfntypes.ChangeActionModify, "RevalidateQueue", "AWS::SQS::Queue", cfntypes.ReplacementTrue))

		err := Run(context.Background(), apis, ClassProduction, everything(), nil, nil)
		if err == nil {
			t.Fatal("a bootstrap that would replace a live queue was allowed through")
		}
		if !strings.Contains(err.Error(), "RevalidateQueue") {
			t.Errorf("refusal = %q, want it to name RevalidateQueue", err)
		}
		if left := cfn.leftBehind(); len(left) != 0 {
			t.Errorf("change sets %v were neither applied nor deleted", left)
		}
	})

	t.Run("accepting replacements writes the stack", func(t *testing.T) {
		cfn, apis := standingBootstrap(t)
		stack := isrStack(ClassProduction)
		cfn.fallBehind(stack)
		cfn.plan(stack, change(cfntypes.ChangeActionModify, "RevalidateQueue", "AWS::SQS::Queue", cfntypes.ReplacementTrue))

		req := everything()
		req.AcceptReplacements = true
		if err := Run(context.Background(), apis, ClassProduction, req, nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if cfn.template(stack) == behindTemplate {
			t.Error("the stack was left at the older template even though replacements were accepted")
		}
	})
}

func TestHeal(t *testing.T) {
	all := HealRequest{Features: featureNames(), Writer: "1.4.0"}

	t.Run("a required feature stack that has fallen behind is written back", func(t *testing.T) {
		cfn, apis := standingBootstrap(t)
		stack := isrStack(ClassProduction)
		cfn.fallBehind(stack)
		var log healLog

		healed, err := Heal(context.Background(), apis, ClassProduction, all, log.write)
		if err != nil {
			t.Fatalf("Heal: %v", err)
		}
		if !healed {
			t.Fatal("a stale feature stack was left behind")
		}
		if cfn.template(stack) == behindTemplate {
			t.Error("the stale template is still what the account carries")
		}
		if !log.says("refreshed " + stack) {
			t.Errorf("heal said %v, want it to name what it refreshed", log.lines)
		}
	})

	t.Run("core never heals", func(t *testing.T) {
		cfn, apis := standingBootstrap(t)
		cfn.fallBehind(StackName)
		var log healLog

		healed, err := Heal(context.Background(), apis, ClassProduction, all, log.write)
		if err != nil {
			t.Fatalf("Heal: %v", err)
		}
		if healed || cfn.template(StackName) != behindTemplate {
			t.Error("the bootstrap's core was rewritten by a heal; only an explicit bootstrap may write it")
		}
	})

	t.Run("a stack this deploy does not need is left alone", func(t *testing.T) {
		cfn, apis := standingBootstrap(t)
		stack := optStack(ClassProduction)
		cfn.fallBehind(stack)
		var log healLog

		healed, err := Heal(context.Background(), apis, ClassProduction, HealRequest{Features: []string{FeatureISR}, Writer: "1.4.0"}, log.write)
		if err != nil {
			t.Fatalf("Heal: %v", err)
		}
		if healed || cfn.template(stack) != behindTemplate {
			t.Error("a feature stack no required feature names was refreshed anyway")
		}
	})

	t.Run("a change the rule refuses leaves the stack as it stands", func(t *testing.T) {
		cfn, apis := standingBootstrap(t)
		stack := isrStack(ClassProduction)
		cfn.fallBehind(stack)
		cfn.plan(stack, change(cfntypes.ChangeActionRemove, "RevalidateQueue", "AWS::SQS::Queue", cfntypes.ReplacementFalse))
		var log healLog

		healed, err := Heal(context.Background(), apis, ClassProduction, all, log.write)
		if err != nil {
			t.Fatalf("Heal: %v", err)
		}
		if healed || cfn.template(stack) != behindTemplate {
			t.Error("a refused change was applied anyway")
		}
		if !log.says("RevalidateQueue") {
			t.Errorf("heal said %v, want it to name what stopped it", log.lines)
		}
		if left := cfn.leftBehind(); len(left) != 0 {
			t.Errorf("change sets %v were neither applied nor deleted", left)
		}
	})

	t.Run("a stack another run is writing is left to that run", func(t *testing.T) {
		holdNothing(t)
		cfn, apis := standingBootstrap(t)
		stack := isrStack(ClassProduction)
		cfn.fallBehind(stack)
		cfn.busy(stack, changeSetAttempts*2)
		var log healLog

		healed, err := Heal(context.Background(), apis, ClassProduction, all, log.write)
		if err != nil {
			t.Fatalf("Heal: %v", err)
		}
		if healed || cfn.template(stack) != behindTemplate {
			t.Error("a stack another run was still writing was written over")
		}
		if !log.says("still being written") {
			t.Errorf("heal said %v, want it to say the stack was still being written", log.lines)
		}
	})

	t.Run("a stack that settles still behind is left to whoever wrote it", func(t *testing.T) {
		holdNothing(t)
		cfn, apis := standingBootstrap(t)
		stack := isrStack(ClassProduction)
		cfn.fallBehind(stack)
		cfn.busy(stack, 3)
		var log healLog

		healed, err := Heal(context.Background(), apis, ClassProduction, all, log.write)
		if err != nil {
			t.Fatalf("Heal: %v", err)
		}
		if healed || cfn.template(stack) != behindTemplate {
			t.Error("a stack that had just been written by another run was written over")
		}
		if !log.says("still behind") {
			t.Errorf("heal said %v, want it to say the stack settled behind what this build carries", log.lines)
		}
	})
}
