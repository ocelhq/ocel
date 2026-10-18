package providerserver_test

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/functioncode"
	"github.com/ocelhq/ocel/pkg/router"
)

func lastPlan(events []*progressv1.OperationEvent) *planv1.ChangePlan {
	var plan *planv1.ChangePlan
	for _, event := range events {
		if got := event.GetPlan(); got != nil {
			plan = got
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
		"stack:prod--web--<release minted at deploy>",
		"stack:prod--admin--<release minted at deploy>",
		"edge:relay/edge",
		"promotion:" + router.DefaultPointer,
	}
	for run, got := range drawn {
		if !slices.Equal(got, want) {
			t.Fatalf("run %d drew %v, want %v: shared infra, then the apps in manifest order, then the edge and the promotion — whatever order the apps finish in, and each app's stack named for a release the deploy mints rather than one this plan invented",
				run, got, want)
		}
	}
}

func TestADryDeployPromotesOneReleasePerApp(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := twoAppRequest()
	req.Dry = true
	result, events := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	promotion := groupOfKind(lastPlan(events), "promotion")
	if got, want := changeNames(promotion), []string{"release:web", "release:admin"}; !slices.Equal(got, want) {
		t.Errorf("the promotion group shows %v, want %v: a promotion moves the pointer to one release of each app", got, want)
	}
}

func TestADryDeployDrawsThePlanAndChangesNothing(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)
	records := p.KeyValues().(*fake.KeyValues)
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
	for _, want := range []string{"parameters:values", "edge:relay/edge", "promotion:" + router.DefaultPointer} {
		if !slices.Contains(names, want) {
			t.Errorf("the plan shows %v, want a %q row: every remote mutation the apply makes is on the plan", names, want)
		}
	}
	stacks := 0
	for _, group := range plan.GetGroups() {
		if group.GetKind() == provider.StackGroupKind {
			stacks++
		}
	}
	if stacks != 2 {
		t.Errorf("the plan shows %d stack groups, want the infra stack and the one app stack", stacks)
	}
	if group := groupOfKind(plan, provider.StackGroupKind); group != nil {
		if !slices.Contains(changeNames(group), "postgres:orders") {
			t.Errorf("the infra group shows %v, want the resource the manifest declares", changeNames(group))
		}
	}
	uploads := false
	for _, group := range plan.GetGroups() {
		if slices.Contains(changeNames(group), functioncode.Kind+":server") {
			uploads = true
		}
	}
	if !uploads {
		t.Errorf("the plan shows %v with no artifact upload row, want the upload the apply would make", groupNames(plan))
	}

	if provisioned := p.FakeStacks().Provisioned(); len(provisioned) != 0 {
		t.Errorf("a dry deploy provisioned %d stacks, want a run that provisions nothing", len(provisioned))
	}
	if reconciled := p.Edges().(*fake.Edges).Edge(fake.KindRelay).Stacks(); len(reconciled) != 0 {
		t.Errorf("a dry deploy reconciled the edge %d times, want it left alone", len(reconciled))
	}
	if after := records.Snapshot(); !maps.Equal(before, after) {
		t.Errorf("a dry deploy wrote records: before %v, after %v",
			slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)))
	}
}

type addingParts struct {
	*fake.Provider
	seen    []provider.DeployPreflight
	applied int
}

func (p *addingParts) Hooks() provider.Hooks {
	hooks := p.Provider.Hooks()
	hooks.Preflight = &provider.PreflightHooks{Plan: p.PlanPreflight, Apply: p.ApplyPreflight}
	return hooks
}

func (p *addingParts) PlanPreflight(_ context.Context, pre provider.DeployPreflight) (provider.Plan, error) {
	p.seen = append(p.seen, pre)
	return provider.Plan{Groups: []provider.ChangeGroup{{Kind: provider.StackGroupKind, Name: "fake/tasks", Part: "tasks", Action: provider.ActionCreate}}}, nil
}

func (p *addingParts) ApplyPreflight(_ context.Context, pre provider.DeployPreflight) error {
	p.seen = append(p.seen, pre)
	p.applied++
	return nil
}

func TestADryDeployPlansThePartsItsPreflightWouldAddBeforeAnythingElse(t *testing.T) {
	builtProject(t)
	declaresNeed(t, "web", edge.NeedRevalidation)
	vendor := &addingParts{Provider: fake.NewProvider(fake.Options{})}
	client := servedBy(t, vendor)

	req := twoAppRequest()
	req.Dry = true
	result, events := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if got := groupNames(lastPlan(events)); len(got) == 0 || got[0] != "stack:fake/tasks" {
		t.Errorf("the plan shows %v, want the part the deploy adds on first use as its first row: it is made before anything that runs on it", got)
	}
	if len(vendor.seen) != 1 {
		t.Fatalf("the preflight ran %d times, want once", len(vendor.seen))
	}
	if uses := vendor.seen[0].Uses; !slices.ContainsFunc(uses, func(u buildoutput.Uses) bool { return u.Revalidation }) {
		t.Errorf("the preflight was handed uses %+v, want the app that revalidates among them: a provider adds what revalidation runs on", uses)
	}
}
