package cloudflare

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

func listProjectResources(t *testing.T, req provider.ProjectResourcesRequest) []provider.PhysicalResource {
	t.Helper()
	listed, err := ListProjectResources(t.Context(), defaultNamespace, fixedOrigin(testOrigin), req)
	if err != nil {
		t.Fatalf("ListProjectResources: %v", err)
	}
	return listed
}

func TestAProjectListsNoEntryWorkerAnotherOriginNamed(t *testing.T) {
	t.Parallel()

	theirs, err := otherNames.entryWorker("shop", environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	got := listProjectResources(t, provider.ProjectResourcesRequest{
		Project: "shop", Tier: environment.TierProduction, EdgeKind: Kind,
		EdgeStack: recordedStack(t, "shop", environment.TierProduction, private{EntryWorker: theirs}),
	})
	if len(got) != 0 {
		t.Errorf("ListProjectResources = %+v, want none: %s is %s's worker", got, theirs, otherOrigin)
	}
}

func recordedStack(t *testing.T, slug string, tier environment.Tier, own private) edge.StackState {
	t.Helper()
	raw, err := json.Marshal((&stack{state: edge.StackState{Slug: slug, Tier: tier}, own: own}).State())
	if err != nil {
		t.Fatal(err)
	}
	var state edge.StackState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func ownEntryWorker(t *testing.T, slug string, tier environment.Tier) string {
	t.Helper()
	name, err := testNames.entryWorker(slug, tier)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func workerResources(resources []provider.PhysicalResource) []provider.PhysicalResource {
	return slices.DeleteFunc(slices.Clone(resources), func(r provider.PhysicalResource) bool {
		return r.Kind != "cloudflare:worker"
	})
}

func TestProjectResourcesNameTheWorkerScriptsTheProjectRunsOnCloudflare(t *testing.T) {
	for _, tc := range []struct {
		tier environment.Tier
		want string
	}{
		{environment.TierProduction, ownEntryWorker(t, "shop", environment.TierProduction)},
		{environment.TierPreview, ownEntryWorker(t, "shop", environment.TierPreview)},
	} {
		got := listProjectResources(t, provider.ProjectResourcesRequest{
			Tier:      tc.tier,
			Project:   "shop",
			EdgeKind:  Kind,
			EdgeStack: recordedStack(t, "shop", tc.tier, private{EntryWorker: tc.want, EnvelopeKey: "k"}),
		})
		want := []provider.PhysicalResource{{Kind: "cloudflare:worker", ID: tc.want, Location: "global"}}
		if !slices.Equal(got, want) {
			t.Errorf("%s: ListProjectResources = %+v, want %+v", tc.tier, got, want)
		}
	}
}

func TestProjectResourcesNameNoWorkerTheProjectDoesNotOwn(t *testing.T) {
	for name, state := range map[string]edge.StackState{
		"shared preview entry": recordedStack(t, "shop", environment.TierPreview, private{EntryWorker: previewEntryScriptName}),
		"prune-only stack":     recordedStack(t, "shop", environment.TierPreview, private{EnvelopeKey: "k"}),
		"proxy stack":          {Slug: "shop", Tier: environment.TierProduction, Bound: []string{"shop.example.com"}},
		"another project":      recordedStack(t, "shop", environment.TierProduction, private{EntryWorker: ownEntryWorker(t, "cart", environment.TierProduction)}),
	} {
		got := listProjectResources(t, provider.ProjectResourcesRequest{
			Tier:      state.Tier,
			Project:   "shop",
			EdgeKind:  Kind,
			EdgeStack: state,
		})
		if len(got) != 0 {
			t.Errorf("%s: ListProjectResources = %+v, want none", name, got)
		}
	}
}

func TestProjectResourcesNameTheDNSRecordsOcelWroteInACloudflareZone(t *testing.T) {
	written := []edge.Record{
		{Name: "shop.example.com", Type: edge.RecordTypeCNAME, Value: "d123.cloudfront.net"},
		{Name: "www.shop.example.com", Type: edge.RecordTypeA, Value: "203.0.113.7"},
	}
	got := listProjectResources(t, provider.ProjectResourcesRequest{
		Tier:           environment.TierProduction,
		Project:        "shop",
		EdgeKind:       "cloudfront",
		DNSKind:        provider.DNSKind(Kind),
		WrittenRecords: written,
	})
	want := []provider.PhysicalResource{
		{Kind: "cloudflare:dns-record", ID: written[0].String(), Location: "global"},
		{Kind: "cloudflare:dns-record", ID: written[1].String(), Location: "global"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("ListProjectResources = %+v, want %+v", got, want)
	}
}

func TestProjectResourcesOfAProjectAnotherEdgeServesAreNone(t *testing.T) {
	got := listProjectResources(t, provider.ProjectResourcesRequest{
		Tier:           environment.TierProduction,
		Project:        "shop",
		EdgeKind:       "cloudfront",
		EdgeStack:      recordedStack(t, "shop", environment.TierProduction, private{EntryWorker: ownEntryWorker(t, "shop", environment.TierProduction)}),
		DNSKind:        "route53",
		WrittenRecords: []edge.Record{{Name: "shop.example.com", Type: edge.RecordTypeCNAME, Value: "d123.cloudfront.net"}},
	})
	if len(got) != 0 {
		t.Errorf("ListProjectResources = %+v, want none", got)
	}
}

func TestProjectResourcesLinkToNoDashboardPageCloudflareDocumentsNoURLFor(t *testing.T) {
	t.Setenv(envAccountID, "0123456789abcdef0123456789abcdef")
	got := listProjectResources(t, provider.ProjectResourcesRequest{
		Tier:           environment.TierProduction,
		Project:        "shop",
		EdgeKind:       Kind,
		EdgeStack:      recordedStack(t, "shop", environment.TierProduction, private{EntryWorker: ownEntryWorker(t, "shop", environment.TierProduction)}),
		DNSKind:        provider.DNSKind(Kind),
		WrittenRecords: []edge.Record{{Name: "shop.example.com", Type: edge.RecordTypeCNAME, Value: "origin.example.net"}},
	})
	if len(workerResources(got)) != 1 || len(got) != 2 {
		t.Fatalf("ListProjectResources = %+v, want the entry worker and the record", got)
	}
	for _, resource := range got {
		if resource.ConsoleURL != "" {
			t.Errorf("%s %s links to %q; Cloudflare documents no dashboard URL that names one resource of this kind", resource.Kind, resource.ID, resource.ConsoleURL)
		}
	}
}

func TestProjectResourcesNameTheProxiedRecordsACloudflareProxyStackHolds(t *testing.T) {
	proxied := []edge.Record{
		{Name: "shop.example.com", Type: edge.RecordTypeA, Value: "203.0.113.7", Proxied: true},
		{Name: "www.shop.example.com", Type: edge.RecordTypeA, Value: "203.0.113.7", Proxied: true},
	}
	got := listProjectResources(t, provider.ProjectResourcesRequest{
		Tier:           environment.TierProduction,
		Project:        "shop",
		EdgeKind:       Kind,
		EdgeStack:      edge.StackState{Slug: "shop", Tier: environment.TierProduction, Bound: []string{"shop.example.com", "www.shop.example.com"}, Records: proxied},
		DNSKind:        provider.DNSKind(Kind),
		WrittenRecords: proxied[:1],
	})
	want := []provider.PhysicalResource{
		{Kind: "cloudflare:dns-record", ID: proxied[0].String(), Location: "global"},
		{Kind: "cloudflare:dns-record", ID: proxied[1].String(), Location: "global"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("ListProjectResources = %+v, want %+v", got, want)
	}
}
