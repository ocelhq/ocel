package conformance

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
)

func RunBootstrap(t *testing.T, bootstrap provider.Bootstrap, kind edge.Kind) {
	t.Helper()

	ctx := context.Background()
	offered := bootstrap.Parts()
	wanted := applicable(offered, kind)

	t.Run("every part has a name no other part and not the base shares", func(t *testing.T) {
		var named []string
		for _, part := range offered {
			if part.Name == "" || part.Name == provider.BasePart {
				t.Errorf("Parts() offers %+v, and nothing can name it to add or remove it", part)
			}
			if slices.Contains(named, part.Name) {
				t.Errorf("Parts() offers %q twice", part.Name)
			}
			named = append(named, part.Name)
		}
	})

	t.Run("Describe answers for the tier it was asked about", func(t *testing.T) {
		for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
			described, err := bootstrap.Describe(ctx, tier)
			if err != nil {
				t.Fatalf("Describe(%s) = %v", tier, err)
			}
			if described.Tier != tier {
				t.Errorf("Describe(%s) answered for %s", tier, described.Tier)
			}
			for _, stack := range described.Stacks {
				if stack.Name == "" {
					t.Errorf("Describe(%s) returned a stack with no name, and no plan can name it", tier)
				}
			}
		}
	})

	t.Run("Plan answers for the request Apply would be given", func(t *testing.T) {
		tier := environment.TierProduction
		described, err := bootstrap.Describe(ctx, tier)
		if err != nil {
			t.Fatalf("Describe(%s) = %v", tier, err)
		}
		plan, err := bootstrap.Plan(ctx, provider.BootstrapRequest{Tier: tier})
		if err != nil {
			t.Fatalf("Plan(%s) = %v", tier, err)
		}
		creates := 0
		for _, group := range plan.Groups {
			if group.Name == "" {
				t.Errorf("Plan() returned %+v, and no plan can render a nameless group", group)
			}
			if !provider.ValidChangeAction(group.Action) {
				t.Errorf("Plan() returned group action %q, which is none the plan knows", group.Action)
			}
			if group.Action == provider.ActionUpdate && len(group.Changes) == 0 && group.Reason == "" {
				t.Errorf("Plan() returned %q as an update with neither children nor a reason, which reads as no change at all", group.Name)
			}
			if group.Action == provider.ActionCreate {
				creates++
			}
			for _, change := range group.Changes {
				if change.Name == "" {
					t.Errorf("Plan() returned %+v under %q, and no plan can render a nameless change", change, group.Name)
				}
				if !provider.ValidChangeAction(change.Action) {
					t.Errorf("Plan() returned change action %q, which is none the plan knows", change.Action)
				}
			}
		}
		if !described.Present && creates == 0 {
			t.Error("Plan() against an account with no bootstrap creates nothing, and an apply from here would create the whole bootstrap")
		}
	})

	t.Run("what Apply installs, Describe reports and Remove takes down", func(t *testing.T) {
		tier := environment.TierPreview
		raising := wanted
		if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, Parts: raising}, nil); err != nil {
			t.Fatalf("Apply() of what the %q edge installs (%v) = %v", kind, raising, err)
		}

		described, err := bootstrap.Describe(ctx, tier)
		if err != nil {
			t.Fatalf("Describe() after Apply() = %v", err)
		}
		for _, part := range unasked(described, raising) {
			t.Errorf("Describe() reports a stack for %q, which Apply() was never asked for", part)
		}
		parts, err := bootstrap.DescribeParts(ctx, described)
		if err != nil {
			t.Fatalf("DescribeParts() after Apply() = %v", err)
		}
		if len(parts) == 0 || parts[0].Name != provider.BasePart {
			t.Fatalf("DescribeParts() after Apply() = %+v, want the base first", parts)
		}
		if len(parts[0].Resources) == 0 {
			t.Errorf("DescribeParts() reports the base with no resources, and `ocel bootstrap status --resources` could name none of what it holds")
		}
		for _, part := range parts {
			if part.Name == "" {
				t.Errorf("DescribeParts() returned %+v, and no status can name a nameless part", part)
			}
			for _, resource := range part.Resources {
				if resource.Kind == "" || resource.ID == "" {
					t.Errorf("DescribeParts() reports %s holding %+v, a resource nobody can find", part.Name, resource)
				}
			}
		}

		t.Run("Plan shows a removed part leaving", func(t *testing.T) {
			var held []string
			for _, part := range parts {
				if part.Name != provider.BasePart {
					held = append(held, part.Name)
				}
			}
			if len(held) == 0 {
				t.Skipf("the %s tier holds no part beside the base, so no removal takes one down", tier)
			}
			plan, err := bootstrap.Plan(ctx, provider.BootstrapRequest{Tier: tier, Remove: held})
			if err != nil {
				t.Fatalf("Plan(%s, drop %v) = %v", tier, held, err)
			}
			leaving := map[string]provider.ChangeAction{}
			for _, group := range plan.Groups {
				if group.Part != "" {
					leaving[group.Part] = group.Action
				}
			}
			for _, name := range held {
				action, planned := leaving[name]
				if !planned {
					t.Errorf("Plan() drops %q and shows no group for it; the plan is the only thing asked about before the apply", name)
					continue
				}
				if action != provider.ActionDelete {
					t.Errorf("Plan() shows %q as %q though it was dropped, and a drop takes its stack down", name, action)
				}
			}
		})

		removal, err := bootstrap.PlanRemove(ctx, tier)
		if err != nil {
			t.Fatalf("PlanRemove() = %v", err)
		}
		for _, group := range removal.Groups {
			if group.Kind == "" || group.Name == "" {
				t.Errorf("PlanRemove() returned %+v, and a removal plan cannot render a nameless group", group)
			}
			if !provider.ValidChangeAction(group.Action) {
				t.Errorf("PlanRemove() returned action %q, which is none the plan knows", group.Action)
			}
			for _, change := range group.Changes {
				if change.Kind == "" || change.Name == "" {
					t.Errorf("PlanRemove() returned row %+v, and a removal plan cannot render a nameless row", change)
				}
				if !provider.ValidChangeAction(change.Action) {
					t.Errorf("PlanRemove() returned row action %q, which is none the plan knows", change.Action)
				}
			}
		}

		if err := bootstrap.Remove(ctx, tier, nil); err != nil {
			t.Fatalf("Remove() = %v", err)
		}
		gone, err := bootstrap.Describe(ctx, tier)
		if err != nil {
			t.Fatalf("Describe() after Remove() = %v", err)
		}
		if gone.Present {
			t.Error("Describe() still reports the bootstrap present after Remove()")
		}
	})

	t.Run("Apply takes a removal of the parts it was given", func(t *testing.T) {
		tier := environment.TierPreview
		if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, Remove: wanted}, nil); err != nil {
			t.Fatalf("Apply() removing what the %q edge installs (%v) = %v", kind, wanted, err)
		}
		if err := bootstrap.Remove(ctx, tier, nil); err != nil {
			t.Fatalf("Remove() = %v", err)
		}
	})
}

func applicable(offered []provider.Part, kind edge.Kind) []string {
	required := bootstrapplan.RequiredParts(offered, kind)
	var wanted []string
	for _, part := range offered {
		if len(part.Edges) == 0 || slices.Contains(required, part.Name) {
			wanted = append(wanted, part.Name)
		}
	}
	return wanted
}

func unasked(described provider.BootstrapDescription, raised []string) []string {
	var parts []string
	for _, stack := range described.Stacks {
		if stack.Present && stack.Part != "" && !slices.Contains(raised, stack.Part) {
			parts = append(parts, stack.Part)
		}
	}
	return parts
}
