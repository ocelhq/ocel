package vps_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
)

type reached struct {
	planned int
	applied int
	removed int
}

func (r *reached) Catalogue() []provider.Feature { return nil }

func (r *reached) Describe(context.Context, environment.Tier) (provider.BootstrapDescription, error) {
	return provider.BootstrapDescription{}, nil
}

func (r *reached) Plan(context.Context, provider.BootstrapRequest) (provider.Plan, error) {
	r.planned++
	return provider.Plan{}, nil
}

func (r *reached) Apply(context.Context, provider.BootstrapRequest, progress.Log) error {
	r.applied++
	return nil
}

func (r *reached) PlanRemove(context.Context, environment.Tier) (provider.Plan, error) {
	r.planned++
	return provider.Plan{}, nil
}

func (r *reached) Remove(context.Context, environment.Tier, progress.Log) error {
	r.removed++
	return nil
}

func unelevated(context.Context) error {
	return refusal.Refuse(refusal.CodeDenied,
		"ocel-deploy@box.example can neither act as root nor run sudo without a password")
}

func TestNothingWritesAsRootWithoutTheGrantToDoIt(t *testing.T) {
	t.Parallel()

	inner := &reached{}
	ctx := context.Background()
	gated := vps.Elevating(inner, unelevated)
	req := provider.BootstrapRequest{Tier: environment.TierProduction}

	if err := gated.Apply(ctx, req, nil); err == nil {
		t.Error("Apply() = nil, want a bootstrap refused rather than failing partway through the first sudo it runs")
	}
	if err := gated.Remove(ctx, environment.TierProduction, nil); err == nil {
		t.Error("Remove() = nil, want a destroy refused: it takes the accounts and the directories a bootstrap wrote as root")
	}
	if inner.applied+inner.removed != 0 {
		t.Errorf("the host was written to %+v through a login that cannot act as root, want nothing attempted", *inner)
	}
}

func TestAskingWhatABootstrapWouldDoIsNotAskingToRunIt(t *testing.T) {
	t.Parallel()

	inner := &reached{}
	ctx := context.Background()
	gated := vps.Elevating(inner, unelevated)

	if _, err := gated.Plan(ctx, provider.BootstrapRequest{Tier: environment.TierProduction}); err != nil {
		t.Fatalf("Plan() = %v, want the plan drawn: reporting what a bootstrap would write is a read, and the same read backs the preflight that answers a deploy's domain claims, bootstrap state and known slugs",
			err)
	}
	if _, err := gated.PlanRemove(ctx, environment.TierProduction); err != nil {
		t.Fatalf("PlanRemove() = %v, want the removal plan drawn for a login that may not run it", err)
	}
	if inner.planned != 2 {
		t.Errorf("the host was planned against %+v, want both questions passed through", *inner)
	}
}

func TestARepairDoesNotNeedWhatABootstrapNeeds(t *testing.T) {
	t.Parallel()

	inner := &reached{}
	ctx := context.Background()
	gated := vps.Elevating(inner, unelevated)
	repairing := provider.BootstrapRequest{Tier: environment.TierProduction, Repair: true}

	if _, err := gated.Plan(ctx, repairing); err != nil {
		t.Fatalf("Plan(repair) = %v, want it planned: repair reasserts only what the deploy login already owns, and that login has no passwordless sudo by design", err)
	}
	if err := gated.Apply(ctx, repairing, nil); err != nil {
		t.Fatalf("Apply(repair) = %v, want the deploy login's own tier reasserted without asking for root", err)
	}
	if inner.planned != 1 || inner.applied != 1 {
		t.Errorf("the host was reached %+v, want the repair passed through once each", *inner)
	}
}
