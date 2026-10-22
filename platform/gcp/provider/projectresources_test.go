package gcp

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func listProjectResources(t *testing.T, req provider.ProjectResourcesRequest) []provider.PhysicalResource {
	t.Helper()
	list := listingProvider().Hooks().ListProjectResources
	if list == nil {
		t.Fatal("Hooks().ListProjectResources is nil")
	}
	listed, err := list(t.Context(), req)
	if err != nil {
		t.Fatalf("ListProjectResources() = %v", err)
	}
	return listed
}

func TestProjectResourcesNameTheCertificatesOcelRequested(t *testing.T) {
	t.Parallel()

	listed := listProjectResources(t, provider.ProjectResourcesRequest{
		Tier: environment.TierProduction, Project: "shop",
		Certificates: []provider.Certificate{
			{ID: "projects/acme-prod/locations/global/certificates/ocel-shop-example-com", Requested: true},
			{ID: "projects/acme-prod/locations/global/certificates/brought-by-the-customer"},
			{Requested: true},
		},
	})

	want := []provider.PhysicalResource{{Kind: "certificatemanager:certificate", ID: "ocel-shop-example-com", Location: "global"}}
	if !slices.Equal(listed, want) {
		t.Errorf("ListProjectResources() = %+v, want %+v: a certificate Ocel did not request, or never got issued, is not one it holds", listed, want)
	}
}

func TestProjectResourcesNameTheWorkersACloudflareEdgeRuns(t *testing.T) {
	t.Parallel()

	listed := listProjectResources(t, provider.ProjectResourcesRequest{
		Tier: environment.TierProduction, Project: "shop", EdgeKind: cloudflare.Kind,
		EdgeStack: edge.StackState{Slug: "shop", Tier: environment.TierProduction, Private: edge.Own(map[string]string{"entryWorker": "ocel-prod-bc35fb-entry-shop-301003"})},
	})

	want := []provider.PhysicalResource{{Kind: "cloudflare:worker", ID: "ocel-prod-bc35fb-entry-shop-301003", Location: "global"}}
	if !slices.Equal(listed, want) {
		t.Errorf("ListProjectResources() = %+v, want %+v", listed, want)
	}
}

func TestProjectResourcesNameTheEntryNEGAndBackendServiceAnALBEdgeHoldsForAHost(t *testing.T) {
	t.Parallel()
	state := edge.StackState{Slug: "shop", Tier: environment.TierProduction, Private: edge.Own(map[string]any{
		"hosts": map[string]any{"shop.example.com": map[string]string{
			"certificate": "projects/acme-prod/locations/global/certificates/ocel-shop-example-com",
			"service":     "ocel-shop-prod-web-1a2b3c",
			"backend":     "ocel-alb-shop-production-shop-example-com",
		}},
	})}
	held, err := alb.ReadHostResources(string(googletest.Names.Namespace()), state)
	if err != nil || len(held) != 1 {
		t.Fatalf("ReadHostResources() = %+v, %v", held, err)
	}

	listed := listProjectResources(t, provider.ProjectResourcesRequest{Tier: environment.TierProduction, Project: "shop", EdgeKind: alb.Kind, EdgeStack: state})

	want := []provider.PhysicalResource{
		{Kind: "certificatemanager:certificatemapentry", ID: held[0].CertificateMapEntry, Location: "global"},
		{Kind: "compute:networkendpointgroup", ID: held[0].NetworkEndpointGroup, Location: "europe-west1"},
		{Kind: "compute:backendservice", ID: "ocel-alb-shop-production-shop-example-com", Location: "global"},
	}
	if !slices.Equal(listed, want) {
		t.Errorf("ListProjectResources() = %+v, want %+v", listed, want)
	}
}

func TestProjectResourcesAreNoneForAProjectServedStraightFromCloudRun(t *testing.T) {
	t.Parallel()

	listed := listProjectResources(t, provider.ProjectResourcesRequest{Tier: environment.TierPreview, Project: "shop", EdgeKind: edge.None})

	if len(listed) != 0 {
		t.Errorf("ListProjectResources() = %+v, want nothing: Cloud Run's own urls hold no resource outside the stacks", listed)
	}
}
