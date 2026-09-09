package gcp

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

type frontRegistry struct {
	opened []edge.Kind
	front  *countingFront
}

func (r *frontRegistry) Supported() []edge.Kind { return []edge.Kind{alb.Kind} }

func (r *frontRegistry) Default() edge.Kind { return alb.Kind }

func (r *frontRegistry) Open(kind edge.Kind) (edge.Edge, error) {
	r.opened = append(r.opened, kind)
	return r.front, nil
}

type countingFront struct {
	*alb.Edge
	raised []edge.Class
	torn   []edge.Class
}

func (f *countingFront) Bootstrap(_ context.Context, class edge.Class) (edge.BootstrapOutput, error) {
	f.raised = append(f.raised, class)
	return edge.BootstrapOutput{}, nil
}

func (f *countingFront) Teardown(_ context.Context, class edge.Class) error {
	f.torn = append(f.torn, class)
	return nil
}

func fronting(t *testing.T) (bootstrapper, *frontRegistry) {
	t.Helper()
	registry := &frontRegistry{front: &countingFront{}}
	return bootstrapper{fronts: registry}, registry
}

func TestABootstrapThatNamedNoEdgeFeatureTakesNoFrontDown(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	if err := b.tearFronts(context.Background(), providerkit.ClassProduction, nil); err != nil {
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
	if err := b.tearFronts(context.Background(), providerkit.ClassProduction, []string{albFeature}); err != nil {
		t.Fatalf("tearFronts = %v", err)
	}
	if !slices.Contains(registry.front.torn, providerkit.ClassProduction) {
		t.Errorf("the removal tore down %v, want the production front: a forwarding rule left standing keeps billing", registry.front.torn)
	}
}

func TestTheFrontsABootstrapRaisesComeFromWhatItsFeaturesDeclareTheyNeed(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	req := providerkit.BootstrapRequest{Class: providerkit.ClassProduction, Features: []string{albFeature}}
	if err := b.raiseFronts(context.Background(), req, nil); err != nil {
		t.Fatalf("raiseFronts = %v", err)
	}
	if !slices.Equal(registry.opened, []edge.Kind{alb.Kind}) {
		t.Errorf("the bootstrap opened %v, want the edge the feature's Needs name: a second edge with a front of its own must not need a branch here",
			registry.opened)
	}
}
