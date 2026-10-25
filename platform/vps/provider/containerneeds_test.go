package vps_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
)

func TestANextContainerOnABoxServingPrerendersNeedsAWaiverForTheEdgeCache(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")
	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	needs := []edge.Need{edge.NeedEdgeCache, edge.NeedStreaming, edge.NeedRevalidation}

	for _, front := range []edge.Kind{edge.None, "cloudflare"} {
		_, err := conformance.CheckContainerNeeds(t, p, front, needs)
		if unsupported := (*providerserver.UnsupportedNeedError)(nil); !errors.As(err, &unsupported) || unsupported.Need != edge.NeedEdgeCache {
			t.Errorf("the need check behind %q = %v, want %s refused: nothing in front of a box caches what its container answers", front, err, edge.NeedEdgeCache)
		}
		if _, err := conformance.CheckContainerNeeds(t, p, front, needs, string(edge.NeedEdgeCache)); err != nil {
			t.Errorf("the need check behind %q with %s waived = %v, want it to pass", front, edge.NeedEdgeCache, err)
		}
	}
}

func TestNoFrontOfABoxDispatchesANextContainer(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")
	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})

	for _, front := range []edge.Kind{edge.None, "cloudflare"} {
		if dispatched := conformance.ListDispatchedComputes(t, p, front); slices.Contains(dispatched, provider.ComputeContainer) {
			t.Errorf("computes %q dispatches = %v, want no %s: a box's front passes a container's requests through", front, dispatched, provider.ComputeContainer)
		}
	}
}
