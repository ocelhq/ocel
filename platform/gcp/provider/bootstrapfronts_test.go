package gcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

type frontRegistry struct {
	opened []edge.Kind
	front  *countingFront
}

func (r *frontRegistry) Open(kind edge.Kind) (edge.Edge, error) {
	r.opened = append(r.opened, kind)
	return r.front, nil
}

type countingFront struct {
	*alb.Edge
	raised   []edge.Class
	torn     []edge.Class
	standing bool
	bound    []string
	refusal  error
	silent   bool
}

func (f *countingFront) Hooks() edge.Hooks {
	if f.silent {
		return edge.Hooks{}
	}
	return edge.Hooks{
		CheckBootstrapStands: func(context.Context, edge.Class) (bool, error) { return f.standing, nil },
		ListBoundHostnames:   func(context.Context, edge.Class) ([]string, error) { return f.bound, nil },
	}
}

func (f *countingFront) Bootstrap(_ context.Context, class edge.Class) (edge.BootstrapOutput, error) {
	f.raised = append(f.raised, class)
	return edge.BootstrapOutput{}, nil
}

func (f *countingFront) Teardown(_ context.Context, class edge.Class) error {
	if f.refusal != nil {
		return f.refusal
	}
	f.torn = append(f.torn, class)
	return nil
}

func fronting(t *testing.T) (bootstrap, *frontRegistry) {
	t.Helper()
	registry := &frontRegistry{front: &countingFront{}}
	return bootstrap{fronts: registry}, registry
}

func surveyed(features ...string) survey {
	return survey{
		Names:   Names{namespace: "ocel", project: "acme-prod"},
		Class:   edge.ClassProduction,
		Project: "acme-prod",
		Present: true,
		Stamp:   stamp{State: stateComplete, Features: features},
	}
}

func featureStack(described providerkit.BootstrapReading, name string) (providerkit.BootstrapStack, bool) {
	for _, stack := range described.Stacks {
		if stack.Feature == name {
			return stack, true
		}
	}
	return providerkit.BootstrapStack{}, false
}

func TestAFeatureWhoseFrontNeverCameUpIsReportedAbsentSoTheGateRaisesItAgain(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.standing = false

	described, err := b.described(context.Background(), surveyed(albFeature))
	if err != nil {
		t.Fatalf("described = %v", err)
	}
	held, reported := featureStack(described, albFeature)
	if !reported {
		t.Fatalf("Describe reports %+v, want a stack for the %q the stamp records", described.Stacks, albFeature)
	}
	if held.Present {
		t.Error("a feature the stamp records is reported standing whatever the front says, so a raise that failed after the stamp was written " +
			"reads as healthy and is never tried again")
	}
}

func TestAFeatureWhoseFrontStandsIsReportedStanding(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.standing = true

	described, err := b.described(context.Background(), surveyed(albFeature))
	if err != nil {
		t.Fatalf("described = %v", err)
	}
	if held, _ := featureStack(described, albFeature); !held.Present {
		t.Error("a feature whose front reported its address and its maps is not reported standing, so every bootstrap raises it again")
	}
}

func TestABootstrapThatNamedNoEdgeFeatureTakesNoFrontDown(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	if err := b.tearFronts(context.Background(), edge.ClassProduction, nil); err != nil {
		t.Fatalf("tearFronts = %v", err)
	}
	if len(registry.opened) != 0 {
		t.Errorf("the removal opened %v, and a class that never stood a load balancer up has no state sealed under a passphrase to read, "+
			"let alone a stack to destroy", registry.opened)
	}
}

func TestABootstrapThatStoodTheLoadBalancerUpTakesItDownAgain(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	if err := b.tearFronts(context.Background(), edge.ClassProduction, []string{albFeature}); err != nil {
		t.Fatalf("tearFronts = %v", err)
	}
	if !slices.Contains(registry.front.torn, edge.ClassProduction) {
		t.Errorf("the removal tore down %v, want the production front: a forwarding rule left standing keeps billing", registry.front.torn)
	}
}

func TestRemovingTheFeatureTakesTheLoadBalancerDownRatherThanJustForgettingIt(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	req := providerkit.BootstrapRequest{Class: edge.ClassProduction, Remove: []string{albFeature}}
	if err := b.dropFronts(context.Background(), surveyed(albFeature), req, nil); err != nil {
		t.Fatalf("dropFronts = %v", err)
	}
	if !slices.Contains(registry.front.torn, edge.ClassProduction) {
		t.Errorf("removing %q tore down %v, and un-stamping a feature whose address, forwarding rule and maps are still standing bills "+
			"for a front nothing will ever take down again", albFeature, registry.front.torn)
	}
}

func TestAFeatureWhoseFrontRefusedToComeDownStaysStamped(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.refusal = refusal.Refuse(refusal.CodeInvalid, "shop.example.com is still bound")
	req := providerkit.BootstrapRequest{Class: edge.ClassProduction, Remove: []string{albFeature}}

	if err := b.dropFronts(context.Background(), surveyed(albFeature), req, nil); err == nil {
		t.Fatal("dropFronts = nil though the front refused, and the apply would go on to un-stamp a feature that is still standing")
	}
	if held := standingFeatures(b.Catalogue(), []string{albFeature}, req); slices.Contains(held, albFeature) {
		t.Errorf("the stamp would still carry %v after a removal, which is only correct because the apply stops on the refusal above", held)
	}
}

func TestRemovingTheFeatureIsRefusedWhileAHostnameIsStillBoundToItsFront(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.bound = []string{"shop.example.com"}
	req := providerkit.BootstrapRequest{Class: edge.ClassProduction, Remove: []string{albFeature}}

	var refusal refusal.Refusal
	err := b.dropFronts(context.Background(), surveyed(albFeature), req, nil)
	if !errors.As(err, &refusal) || !strings.Contains(refusal.Message, "shop.example.com") {
		t.Fatalf("dropFronts with a hostname still bound = %v, want a refusal naming it", err)
	}
	if len(registry.front.torn) != 0 {
		t.Errorf("the refused removal tore down %v: a certificate map with entries cannot be deleted, so the destroy fails partway",
			registry.front.torn)
	}
}

func TestAFeatureNothingStoodUpIsNotTornDownOnRemoval(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	req := providerkit.BootstrapRequest{Class: edge.ClassProduction, Remove: []string{albFeature}}
	if err := b.dropFronts(context.Background(), surveyed(), req, nil); err != nil {
		t.Fatalf("dropFronts = %v", err)
	}
	if len(registry.opened) != 0 {
		t.Errorf("removing a feature the stamp never recorded opened %v, and there is no state sealed under a passphrase to read", registry.opened)
	}
}

func TestTheFrontsABootstrapRaisesComeFromWhatItsFeaturesDeclareTheyNeed(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	req := providerkit.BootstrapRequest{Class: edge.ClassProduction, Features: []string{albFeature}}
	if err := b.raiseFronts(context.Background(), req, nil); err != nil {
		t.Fatalf("raiseFronts = %v", err)
	}
	if !slices.Equal(registry.opened, []edge.Kind{alb.Kind}) {
		t.Errorf("the bootstrap opened %v, want the edge the feature's Needs name: a second edge with a front of its own must not need a branch here",
			registry.opened)
	}
}

func TestAFrontThatReportsNothingStandsAndHoldsNoHostname(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.silent = true
	registry.front.bound = []string{"shop.example.com"}

	stands, err := b.frontStands(context.Background(), edge.ClassProduction, albFeature)
	if err != nil {
		t.Fatalf("frontStands = %v", err)
	}
	if !stands {
		t.Error("a front that reports nothing about its bootstrap is taken for gone, so the stamp alone can no longer say the feature stands")
	}
	if err := b.frontsFree(context.Background(), edge.ClassProduction, []string{albFeature}); err != nil {
		t.Errorf("frontsFree = %v, want nothing held by a front that names no bound hostname", err)
	}
}
