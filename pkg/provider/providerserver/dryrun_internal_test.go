package providerserver

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAGroupTwoAppsBothPlanIsDrawnOnce(t *testing.T) {
	t.Parallel()
	shared := provider.ChangeGroup{Kind: provider.StackGroupKind, Name: "bootstrap part cloudfront", Action: provider.ActionCreate}
	run := &dryRunPlan{apps: []provider.Plan{
		{Groups: []provider.ChangeGroup{shared, {Kind: provider.StackGroupKind, Name: "prod--web", Action: provider.ActionCreate}}},
		{Groups: []provider.ChangeGroup{shared, {Kind: provider.StackGroupKind, Name: "prod--admin", Action: provider.ActionCreate}}},
	}}
	drawn := run.plan()

	var names []string
	for _, group := range drawn.Groups {
		if group.Kind == provider.StackGroupKind {
			names = append(names, group.Name)
		}
	}
	if want := []string{shared.Name, "prod--web", "prod--admin"}; !slices.Equal(names, want) {
		t.Errorf("stack groups = %v, want %v: a piece every app of the deploy needs is made once, so it is shown once", names, want)
	}
}
