package gcp_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

var nextContainerNeeds = []edge.Need{edge.NeedEdgeCache, edge.NeedStreaming, edge.NeedRevalidation}

func TestANextContainerBehindTheLoadBalancerServingPrerendersDeploysWithNoWaiver(t *testing.T) {
	p := newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"})

	verdict, err := conformance.CheckContainerNeeds(t, p, alb.Kind, nil, nextContainerNeeds)
	if err != nil {
		t.Fatalf("the need check = %v, want it to pass: Cloud CDN stores and purges a Next container's tagged pages", err)
	}
	if !slices.Contains(verdict.InEffect, edge.NeedEdgeCache) {
		t.Errorf("needs in effect = %v, want %s", verdict.InEffect, edge.NeedEdgeCache)
	}
}

func TestANextContainerServedByCloudRunDirectlyNeedsAWaiverForTheEdgeCache(t *testing.T) {
	p := newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"})

	_, err := conformance.CheckContainerNeeds(t, p, edge.None, nil, nextContainerNeeds)
	if unsupported := (*providerserver.UnsupportedNeedError)(nil); !errors.As(err, &unsupported) || unsupported.Need != edge.NeedEdgeCache {
		t.Errorf("the need check = %v, want %s refused: Cloud Run stores nothing it serves", err, edge.NeedEdgeCache)
	}
	if _, err := conformance.CheckContainerNeeds(t, p, edge.None, nil, nextContainerNeeds, string(edge.NeedEdgeCache)); err != nil {
		t.Errorf("the need check with %s waived = %v, want it to pass", edge.NeedEdgeCache, err)
	}
}

func TestTheCloudflareWorkerDispatchesANextContainerOnGoogleCloudAndNoOtherFrontRunsItsCode(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")
	p := newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"})

	if dispatched := conformance.ListDispatchedComputes(t, p, cloudflare.Kind, nil); !slices.Contains(dispatched, provider.ComputeContainer) {
		t.Errorf("computes Cloudflare dispatches = %v, want %s: the Worker's router serves a container app as it serves a function", dispatched, provider.ComputeContainer)
	}
	for _, front := range []edge.Kind{edge.None, alb.Kind} {
		if dispatched := conformance.ListDispatchedComputes(t, p, front, nil); slices.Contains(dispatched, provider.ComputeContainer) {
			t.Errorf("computes %q dispatches = %v, want no %s: it runs no code", front, dispatched, provider.ComputeContainer)
		}
	}
}
