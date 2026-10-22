package vps_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
)

func TestProjectResourcesNameTheWorkersACloudflareEdgeRunsForThisBox(t *testing.T) {
	t.Parallel()

	machine := &box{}
	written, err := routing.WriteRoutingTable(boxapi.RoutingTable{Grace: routing.DrainWindow, OriginID: "k3j9x0"})
	if err != nil {
		t.Fatal(err)
	}
	machine.routingDoc = string(written)
	for _, tc := range []struct {
		worker string
		want   []provider.PhysicalResource
	}{
		{worker: "ocel-prod-b8e245-entry-shop-de565c", want: []provider.PhysicalResource{{Kind: "cloudflare:worker", ID: "ocel-prod-b8e245-entry-shop-de565c", Location: "global"}}},
		{worker: "ocel-prod-4c73ed-entry-shop-023da1"},
	} {
		req := provider.ProjectResourcesRequest{
			Tier:     environment.TierProduction,
			Project:  "shop",
			EdgeKind: cloudflare.Kind,
			EdgeStack: edge.StackState{
				Slug: "shop", Tier: environment.TierProduction,
				Private: edge.Own(map[string]string{"entryWorker": tc.worker}),
			},
		}
		listed, err := over(machine).Hooks().ListProjectResources(context.Background(), req)
		if err != nil {
			t.Fatalf("ListProjectResources() = %v", err)
		}
		if !slices.Equal(listed, tc.want) {
			t.Errorf("ListProjectResources(%s) = %+v, want %+v: only the worker named for this box's origin is its own", tc.worker, listed, tc.want)
		}
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
