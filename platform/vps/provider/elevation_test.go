package vps_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
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

func (r *reached) Parts() []provider.Part { return nil }

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

func (r *reached) DescribeParts(context.Context, provider.BootstrapDescription) ([]provider.BootstrapPart, error) {
	return nil, nil
}

func unelevated(context.Context) error {
	return refusal.Refuse(refusal.CodeDenied,
		"ocel-deploy@box.example can neither act as root nor run sudo without a password")
}

var gatedBoxes = map[string]func(inner *reached, gate func(context.Context) error) provider.Bootstrap{
	"a box serving on its own ports": func(inner *reached, gate func(context.Context) error) provider.Bootstrap {
		return vps.Exposed(inner, gate, func(context.Context, environment.Tier) error { return nil })
	},
	"a box behind an edge": func(inner *reached, gate func(context.Context) error) provider.Bootstrap {
		return vps.Fronted(inner, gate, &bootstrappedEdge{applied: inner})
	},
}

func TestNothingWritesAsRootWithoutTheGrantToDoIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	req := provider.BootstrapRequest{Tier: environment.TierProduction}
	for name, gatedBox := range gatedBoxes {
		inner := &reached{}
		gated := gatedBox(inner, unelevated)

		if err := gated.Apply(ctx, req, progress.Discard()); err == nil {
			t.Errorf("%s: Apply() = nil, want a bootstrap refused rather than failing partway through the first sudo it runs", name)
		}
		if err := gated.Remove(ctx, environment.TierProduction, progress.Discard()); err == nil {
			t.Errorf("%s: Remove() = nil, want a destroy refused: it takes the accounts and the directories a bootstrap wrote as root", name)
		}
		if inner.applied+inner.removed != 0 {
			t.Errorf("%s: the host was written to %+v through a login that cannot act as root, want nothing attempted", name, *inner)
		}
	}
}

func TestAskingWhatABootstrapWouldDoIsNotAskingToRunIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	for name, gatedBox := range gatedBoxes {
		inner := &reached{}
		gated := gatedBox(inner, unelevated)

		if _, err := gated.Plan(ctx, provider.BootstrapRequest{Tier: environment.TierProduction}); err != nil {
			t.Fatalf("%s: Plan() = %v, want the plan drawn: reporting what a bootstrap would write is a read, and the same read backs the preflight that answers a deploy's domain claims, bootstrap state and known slugs",
				name, err)
		}
		if _, err := gated.PlanRemove(ctx, environment.TierProduction); err != nil {
			t.Fatalf("%s: PlanRemove() = %v, want the removal plan drawn for a login that may not run it", name, err)
		}
		if inner.planned != 2 {
			t.Errorf("%s: the host was planned against %+v, want both questions passed through", name, *inner)
		}
	}
}

type tornDownEdge struct {
	edge.Edge
	tornDown []environment.Tier
	removed  *reached
}

func (e *tornDownEdge) Kind() edge.Kind { return "cloudflare" }

func (e *tornDownEdge) Hooks() edge.Hooks {
	return edge.Hooks{PlanRemoveBootstrap: func(context.Context, environment.Tier) ([]edge.PlanChange, error) {
		return []edge.PlanChange{{Kind: "Cloudflare::OriginPullCertificate", Name: "app.com/ours", Action: edge.PlanDelete}}, nil
	}}
}

func (e *tornDownEdge) Teardown(_ context.Context, tier environment.Tier) error {
	if e.removed.removed != 0 {
		return errors.New("the box was removed before its edge, and the box holds the origin id the edge names its certificates by")
	}
	e.tornDown = append(e.tornDown, tier)
	return nil
}

func TestDestroyingABoxBootstrapBehindAnEdgeTearsThatEdgesTierDownFirst(t *testing.T) {
	t.Parallel()

	inner := &reached{}
	front := &tornDownEdge{removed: inner}
	gated := vps.Fronted(inner, func(context.Context) error { return nil }, front)

	plan, err := gated.PlanRemove(context.Background(), environment.TierPreview)
	if err != nil {
		t.Fatalf("PlanRemove: %v", err)
	}
	if !slices.ContainsFunc(plan.Groups, func(group provider.ChangeGroup) bool {
		return group.Kind == edge.EdgeGroupKind && slices.ContainsFunc(group.Changes, func(change provider.Change) bool { return change.Name == "app.com/ours" })
	}) {
		t.Errorf("PlanRemove = %+v, want the edge's removals shown, so the destroy consents to the certificates it deletes", plan.Groups)
	}
	if err := gated.Remove(context.Background(), environment.TierPreview, progress.Discard()); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !slices.Equal(front.tornDown, []environment.Tier{environment.TierPreview}) || inner.removed != 1 {
		t.Errorf("tore the edge down for %v and removed the box %d times, want the preview tier's edge torn down, then the box", front.tornDown, inner.removed)
	}
}

type bootstrappedEdge struct {
	edge.Edge
	planRefusal  error
	bootstrapped []environment.Tier
	applied      *reached
}

func (e *bootstrappedEdge) Kind() edge.Kind { return "cloudflare" }

func (e *bootstrappedEdge) Hooks() edge.Hooks {
	return edge.Hooks{PlanBootstrap: func(context.Context, environment.Tier) ([]edge.PlanChange, error) {
		return nil, e.planRefusal
	}}
}

func (e *bootstrappedEdge) Bootstrap(_ context.Context, tier environment.Tier) (edge.BootstrapOutput, error) {
	if e.applied.applied == 0 {
		return edge.BootstrapOutput{}, errors.New("the edge was bootstrapped before the box, and the box holds the origin id the edge names what it owns by")
	}
	e.bootstrapped = append(e.bootstrapped, tier)
	return edge.BootstrapOutput{}, nil
}

func TestBootstrappingABoxBehindAnEdgeBootstrapsThatEdgesTierAfterTheBox(t *testing.T) {
	t.Parallel()

	inner := &reached{}
	front := &bootstrappedEdge{applied: inner}
	gated := vps.Fronted(inner, func(context.Context) error { return nil }, front)

	if err := gated.Apply(context.Background(), provider.BootstrapRequest{Tier: environment.TierPreview}, progress.Discard()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !slices.Equal(front.bootstrapped, []environment.Tier{environment.TierPreview}) {
		t.Errorf("bootstrapped the edge for %v, want the preview tier's, so the edge records the account it bootstrapped against", front.bootstrapped)
	}
}

func TestABoxBootstrapPlanCarriesItsEdgesRefusal(t *testing.T) {
	t.Parallel()

	inner := &reached{}
	refused := refusal.Refuse(refusal.CodeInvalid, "this origin bootstrapped its preview tier on Cloudflare against account acct-first")
	gated := vps.Fronted(inner, unelevated, &bootstrappedEdge{applied: inner, planRefusal: refused})

	_, err := gated.Plan(context.Background(), provider.BootstrapRequest{Tier: environment.TierPreview})

	var got refusal.Refusal
	if !errors.As(err, &got) || got.Code != refusal.CodeInvalid {
		t.Fatalf("Plan() = %v, want the edge's refusal surfaced before anything is applied", err)
	}
}
