package providerkit_test

import (
	"maps"
	"slices"
	"testing"

	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func lastPlan(events []*progressv1.OperationEvent) *planv1.ChangePlan {
	var plan *planv1.ChangePlan
	for _, event := range events {
		if held := event.GetPlan(); held != nil {
			plan = held
		}
	}
	return plan
}

func groupNames(plan *planv1.ChangePlan) []string {
	names := make([]string, 0, len(plan.GetGroups()))
	for _, group := range plan.GetGroups() {
		names = append(names, group.GetKind()+":"+group.GetName())
	}
	return names
}

func changeNames(group *planv1.ChangeGroup) []string {
	names := make([]string, 0, len(group.GetChanges()))
	for _, change := range group.GetChanges() {
		names = append(names, change.GetKind()+":"+change.GetName())
	}
	return names
}

func groupOfKind(plan *planv1.ChangePlan, kind string) *planv1.ChangeGroup {
	for _, group := range plan.GetGroups() {
		if group.GetKind() == kind {
			return group
		}
	}
	return nil
}

func TestADryDeployOrdersItsGroupsTheSameWayEveryRun(t *testing.T) {
	builtProject(t)

	req := twoAppRequest()
	req.Dry = true

	var drawn [][]string
	for range 5 {
		client, _ := deployServed(t)
		result, events := deploy(t, client, req)
		if result == nil || !result.GetSuccess() {
			t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
		}
		plan := lastPlan(events)
		if plan == nil {
			t.Fatal("a dry deploy streamed no plan")
		}
		drawn = append(drawn, groupNames(plan))
	}

	want := []string{
		"stack:prod--infra",
		"parameters:values",
		"stack:prod--web--r7dad1fc0",
		"stack:prod--admin--r027a305d",
		"edge:relay/edge",
		"promotion:" + edge.DefaultPointer,
	}
	for run, got := range drawn {
		if !slices.Equal(got, want) {
			t.Fatalf("run %d drew %v, want %v: shared infra, then the apps in manifest order, then the edge and the promotion — whatever order the apps finish in",
				run, got, want)
		}
	}
}

func TestADryDeployDrawsThePlanAndChangesNothing(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	records := provider.Records().(*fake.Records)
	before := records.Snapshot()

	req := deployRequest()
	req.Dry = true
	result, events := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	plan := lastPlan(events)
	if plan == nil {
		t.Fatal("a dry deploy streamed no plan, and drawing the plan is all it is for")
	}
	names := groupNames(plan)
	for _, want := range []string{"parameters:values", "edge:relay/edge", "promotion:" + edge.DefaultPointer} {
		if !slices.Contains(names, want) {
			t.Errorf("the plan shows %v, want a %q row: every remote mutation the apply makes is on the plan", names, want)
		}
	}
	stacks := 0
	for _, group := range plan.GetGroups() {
		if group.GetKind() == providerkit.StackGroupKind {
			stacks++
		}
	}
	if stacks != 2 {
		t.Errorf("the plan shows %d stack groups, want the infra stack and the one app stack", stacks)
	}
	if group := groupOfKind(plan, providerkit.StackGroupKind); group != nil {
		if !slices.Contains(changeNames(group), "postgres:orders") {
			t.Errorf("the infra group shows %v, want the resource the manifest declares", changeNames(group))
		}
	}
	uploads := false
	for _, group := range plan.GetGroups() {
		if slices.Contains(changeNames(group), providerkit.UploadKind+":server") {
			uploads = true
		}
	}
	if !uploads {
		t.Errorf("the plan shows %v with no artifact upload row, want the upload the apply would make", groupNames(plan))
	}

	if provisioned := provider.Releases().(*fake.Releaser).Plans(); len(provisioned) != 0 {
		t.Errorf("a dry deploy provisioned %d stacks, want a run that stands nothing up", len(provisioned))
	}
	if reconciled := provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Stacks(); len(reconciled) != 0 {
		t.Errorf("a dry deploy reconciled the edge %d times, want it left alone", len(reconciled))
	}
	if after := records.Snapshot(); !maps.Equal(before, after) {
		t.Errorf("a dry deploy wrote records: before %v, after %v",
			slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)))
	}
}
