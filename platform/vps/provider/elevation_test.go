package vps_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
)

type reached struct {
	planned int
	applied int
	removed int
}

func (r *reached) Catalogue() []providerkit.Feature { return nil }

func (r *reached) Describe(context.Context, providerkit.Class) (providerkit.Bootstrap, error) {
	return providerkit.Bootstrap{}, nil
}

func (r *reached) Plan(context.Context, providerkit.BootstrapRequest) (providerkit.Plan, error) {
	r.planned++
	return providerkit.Plan{}, nil
}

func (r *reached) Apply(context.Context, providerkit.BootstrapRequest, providerkit.Reporter) error {
	r.applied++
	return nil
}

func (r *reached) PlanRemoval(context.Context, providerkit.Class) (providerkit.Plan, error) {
	r.planned++
	return providerkit.Plan{}, nil
}

func (r *reached) Remove(context.Context, providerkit.Class, providerkit.Reporter) error {
	r.removed++
	return nil
}

func unelevated(context.Context) error {
	return providerkit.Refuse(providerkit.CodeDenied,
		"ocel-deploy@box.example can neither act as root nor run sudo without a password")
}

func TestNothingWritesAsRootWithoutTheGrantToDoIt(t *testing.T) {
	t.Parallel()

	inner := &reached{}
	ctx := context.Background()
	gated := vps.Elevating(inner, unelevated)
	req := providerkit.BootstrapRequest{Class: providerkit.ClassProduction}

	if err := gated.Apply(ctx, req, nil); err == nil {
		t.Error("Apply() = nil, want a bootstrap refused rather than failing partway through the first sudo it runs")
	}
	if err := gated.Remove(ctx, providerkit.ClassProduction, nil); err == nil {
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

	if _, err := gated.Plan(ctx, providerkit.BootstrapRequest{Class: providerkit.ClassProduction}); err != nil {
		t.Fatalf("Plan() = %v, want the plan drawn: reporting what a bootstrap would write is a read, and the same read backs the preflight that answers a deploy's domain claims, bootstrap standing and known slugs",
			err)
	}
	if _, err := gated.PlanRemoval(ctx, providerkit.ClassProduction); err != nil {
		t.Fatalf("PlanRemoval() = %v, want the removal plan drawn for a login that may not run it", err)
	}
	if inner.planned != 2 {
		t.Errorf("the host was planned against %+v, want both questions carried through", *inner)
	}
}

func TestAHealIsNotHeldToWhatABootstrapNeeds(t *testing.T) {
	t.Parallel()

	inner := &reached{}
	ctx := context.Background()
	gated := vps.Elevating(inner, unelevated)
	healing := providerkit.BootstrapRequest{Class: providerkit.ClassProduction, Heal: true}

	if _, err := gated.Plan(ctx, healing); err != nil {
		t.Fatalf("Plan(heal) = %v, want it planned: heal reasserts only what the deploy login already owns, and that login holds no passwordless sudo by design", err)
	}
	if err := gated.Apply(ctx, healing, nil); err != nil {
		t.Fatalf("Apply(heal) = %v, want the deploy login's own tier reasserted without asking for root", err)
	}
	if inner.planned != 1 || inner.applied != 1 {
		t.Errorf("the host was reached %+v, want the heal carried through once each", *inner)
	}
}
