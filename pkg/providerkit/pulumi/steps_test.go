package pulumi

import (
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func step(op apitype.OpType, kind, name string) apitype.StepEventMetadata {
	return apitype.StepEventMetadata{
		Op:   op,
		Type: kind,
		URN:  "urn:pulumi:prod::ocel-shop::" + kind + "::" + name,
	}
}

func TestEveryStepTheEngineShowsBecomesAPlanRow(t *testing.T) {
	t.Parallel()

	rows := map[string]providerkit.ChangeAction{}
	for _, change := range planRows([]apitype.StepEventMetadata{
		step(apitype.OpCreate, "aws:rds/cluster:Cluster", "orders"),
		step(apitype.OpUpdate, "aws:s3/bucket:Bucket", "uploads"),
		step(apitype.OpDelete, "aws:s3/bucket:Bucket", "exports"),
		step(apitype.OpSame, "aws:iam/role:Role", "app"),
		step(apitype.OpReplace, "aws:rds/instance:Instance", "reporting"),
		step(apitype.OpSame, stackResourceType, "ocel-shop-prod"),
	}) {
		rows[change.Name] = change.Action
	}

	for name, action := range map[string]providerkit.ChangeAction{
		"orders":    providerkit.ActionCreate,
		"uploads":   providerkit.ActionUpdate,
		"exports":   providerkit.ActionDelete,
		"app":       providerkit.ActionKeep,
		"reporting": providerkit.ActionReplace,
	} {
		if rows[name] != action {
			t.Errorf("%s reads %q, want %q", name, rows[name], action)
		}
	}
	if _, carried := rows["ocel-shop-prod"]; carried {
		t.Error("the stack resource itself is a plan row, and nothing in the customer's account answers to it")
	}
}

func TestStepsThatNeverDrainRefuseRatherThanReadAsNoChange(t *testing.T) {
	t.Parallel()

	steps, err := awaitSteps(make(chan []apitype.StepEventMetadata), 20*time.Millisecond)
	if err == nil {
		t.Fatalf("awaitSteps() over a stream that never drained = %v, want an error: a plan drawn from nothing reads as nothing would change", steps)
	}
	if steps != nil {
		t.Errorf("awaitSteps() returned %v beside its error, want nothing", steps)
	}
}
