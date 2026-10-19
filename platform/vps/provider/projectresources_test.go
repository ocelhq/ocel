package vps_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func TestProjectResourcesNameTheWorkersACloudflareEdgeRuns(t *testing.T) {
	t.Parallel()

	machine := &box{}
	req := provider.ProjectResourcesRequest{
		Tier:     environment.TierProduction,
		Project:  "shop",
		EdgeKind: cloudflare.Kind,
		EdgeStack: edge.StackState{
			Slug: "shop", Tier: environment.TierProduction,
			Private: edge.Own(map[string]string{"entryWorker": "ocel-prod-entry-shop-138c36"}),
		},
	}
	listed, err := over(machine).Hooks().ListProjectResources(context.Background(), req)
	if err != nil {
		t.Fatalf("ListProjectResources() = %v", err)
	}
	want := []provider.PhysicalResource{{Kind: "cloudflare:worker", ID: "ocel-prod-entry-shop-138c36", Location: "global"}}
	if !slices.Equal(listed, want) {
		t.Errorf("ListProjectResources() = %+v, want the entry worker Cloudflare runs for the project", listed)
	}
	if ran := machine.commands(); len(ran) > 0 {
		t.Errorf("listing ran %q on the box, want the names read from the request alone", ran)
	}
}

func TestProjectResourcesNameTheDNSRecordsCloudflareHoldsForTheProject(t *testing.T) {
	t.Parallel()

	record := edge.Record{Name: "shop.example.com", Type: edge.RecordTypeA, Value: "203.0.113.10", Proxied: true}
	listed, err := over(&box{}).Hooks().ListProjectResources(context.Background(), provider.ProjectResourcesRequest{
		Tier:           environment.TierProduction,
		Project:        "shop",
		DNSKind:        provider.DNSKind(cloudflare.Kind),
		WrittenRecords: []edge.Record{record},
	})
	if err != nil {
		t.Fatalf("ListProjectResources() = %v", err)
	}
	want := []provider.PhysicalResource{{Kind: "cloudflare:dns-record", ID: record.String(), Location: "global"}}
	if !slices.Equal(listed, want) {
		t.Errorf("ListProjectResources() = %+v, want the record Cloudflare DNS holds for the project", listed)
	}
}
