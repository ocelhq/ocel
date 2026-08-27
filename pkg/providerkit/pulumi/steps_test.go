package pulumi

import (
	"slices"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
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

func mutating(op apitype.OpType, kind, name string) events.EngineEvent {
	return events.EngineEvent{ResourcePreEvent: &apitype.ResourcePreEvent{Metadata: step(op, kind, name)}}
}

func settled(op apitype.OpType, kind, name string) events.EngineEvent {
	return events.EngineEvent{ResOutputsEvent: &apitype.ResOutputsEvent{Metadata: step(op, kind, name)}}
}

func rowsOf(t *testing.T, stream ...events.EngineEvent) []providerkit.Change {
	t.Helper()

	engineEvents := make(chan events.EngineEvent, len(stream))
	drained := drainRows(engineEvents)
	for _, ev := range stream {
		engineEvents <- ev
	}
	close(engineEvents)
	rows, err := awaitRows(drained, time.Second)
	if err != nil {
		t.Fatalf("awaitRows() = %v, want the rows the stream carried", err)
	}
	return rows
}

func TestEveryStepTheEngineShowsBecomesAPlanRow(t *testing.T) {
	t.Parallel()

	rows := map[string]providerkit.ChangeAction{}
	for _, change := range rowsOf(t,
		mutating(apitype.OpCreate, "aws:rds/cluster:Cluster", "orders"),
		mutating(apitype.OpUpdate, "aws:s3/bucket:Bucket", "uploads"),
		mutating(apitype.OpDelete, "aws:s3/bucket:Bucket", "exports"),
		mutating(apitype.OpReplace, "aws:rds/instance:Instance", "reporting"),
		settled(apitype.OpSame, "aws:iam/role:Role", "app"),
		settled(apitype.OpSame, stackResourceType, "ocel-shop-prod"),
	) {
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

func TestAKeepRowComesFromTheEngineOutputsEventAndNowhereElse(t *testing.T) {
	t.Parallel()

	rows := rowsOf(t, settled(apitype.OpSame, "aws:iam/role:Role", "app"))
	if len(rows) != 1 || rows[0].Action != providerkit.ActionKeep || rows[0].Name != "app" {
		t.Fatalf("a stream carrying only an outputs event for a standing resource read as %+v, want one keep row for app", rows)
	}

	silent := rowsOf(t, mutating(apitype.OpSame, "aws:iam/role:Role", "app"))
	if len(silent) != 0 {
		t.Errorf("a pre-event for a standing resource read as %+v, want nothing: a keep is what the engine's outputs say stood", silent)
	}
}

func TestTheSameResourceIsOneRowHoweverManyOutputsEventsItSends(t *testing.T) {
	t.Parallel()

	rows := rowsOf(t,
		settled(apitype.OpSame, "aws:iam/role:Role", "app"),
		settled(apitype.OpSame, "aws:iam/role:Role", "app"),
	)
	if len(rows) != 1 {
		t.Errorf("two outputs events for one standing resource read as %+v, want one row", rows)
	}
}

func TestRowsReadInOneOrderWhateverOrderTheEngineSendsThem(t *testing.T) {
	t.Parallel()

	stream := []events.EngineEvent{
		mutating(apitype.OpCreate, "aws:s3/bucket:Bucket", "uploads"),
		settled(apitype.OpSame, "aws:iam/role:Role", "app"),
		mutating(apitype.OpCreate, "aws:lambda/function:Function", "web"),
		settled(apitype.OpSame, "aws:s3/object:Object", "artifact"),
	}
	want := rowsOf(t, stream...)
	if len(want) != 4 {
		t.Fatalf("the stream read as %+v, want a row for each of its four resources", want)
	}
	for _, order := range [][]int{{3, 2, 1, 0}, {1, 3, 0, 2}, {2, 0, 3, 1}} {
		shuffled := make([]events.EngineEvent, 0, len(stream))
		for _, at := range order {
			shuffled = append(shuffled, stream[at])
		}
		if got := rowsOf(t, shuffled...); !slices.Equal(got, want) {
			t.Errorf("the same stream arriving as %v read as %+v, want %+v", order, got, want)
		}
	}
}

func TestStepsThatNeverDrainRefuseRatherThanReadAsNoChange(t *testing.T) {
	t.Parallel()

	rows, err := awaitRows(make(chan []providerkit.Change), 20*time.Millisecond)
	if err == nil {
		t.Fatalf("awaitRows() over a stream that never drained = %v, want an error: a plan drawn from nothing reads as nothing would change", rows)
	}
	if rows != nil {
		t.Errorf("awaitRows() returned %v beside its error, want nothing", rows)
	}
}
