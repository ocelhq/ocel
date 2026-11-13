package gcp

import (
	"context"
	"maps"
	"os"
	"slices"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/pricing/pricingtest"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

type locationCase struct {
	column     func(location) string
	typ        string
	properties map[string]any
	component  string
}

var locationCases = map[string]locationCase{
	"continent": {func(l location) string { return l.continent }, "google_cloud_run_v2_service", map[string]any{
		"ingress": ingressEverywhere, "template": map[string]any{"containers": []any{map[string]any{"resources": map[string]any{
			"cpu_idle": true, "limits": map[string]any{"cpu": "1", "memory": "512Mi"},
		}}}},
	}, "Data transfer out to internet"},
	"storage":   {func(l location) string { return l.storage }, "google_storage_bucket", map[string]any{}, "Storage"},
	"firestore": {func(l location) string { return l.firestore }, "google_firestore_database", map[string]any{}, "Document reads"},
	"memorystore": {func(l location) string { return l.memorystore }, "google_memorystore_instance", map[string]any{
		"node_type": "CUSTOM_PICO", "shard_count": 1, "replica_count": 0,
	}, "Node"},
	"sql": {func(l location) string { return l.sql }, "google_sql_database_instance", map[string]any{
		"database_version": "POSTGRES_17", "settings": map[string]any{"tier": "db-custom-1-3840", "edition": "ENTERPRISE", "disk_size": 10},
	}, "vCPU"},
	"network": {func(l location) string { return l.network }, "google_compute_global_forwarding_rule", map[string]any{}, "Data processed"},
}

func locationSet(c locationCase) *costv1.ResourceSet {
	set := &costv1.ResourceSet{Source: "terraform", Scopes: []*costv1.Scope{{Id: "stack", Kind: "module", Name: "root"}}}
	for _, region := range slices.Sorted(maps.Keys(locations)) {
		if c.column(locations[region]) == "" {
			continue
		}
		properties, err := structpb.NewStruct(c.properties)
		if err != nil {
			panic(err)
		}
		set.Resources = append(set.Resources, &costv1.Resource{Id: region, Scope: "stack", Vendor: vendor, Type: c.typ, Region: region, Properties: properties})
	}
	return set
}

func TestEveryLocationTheTablesNameIsOneTheCatalogSells(t *testing.T) {
	raw, err := os.ReadFile("testdata/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	card, err := pricingtest.NewCard(raw)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range locationCases {
		t.Run(name, func(t *testing.T) {
			est, err := pricing.Estimate(context.Background(), card, Rules.Table, locationSet(c), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(est.GetResources()) == 0 {
				t.Fatal("no row names this column")
			}
			for _, r := range est.GetResources() {
				for _, component := range r.GetComponents() {
					if component.GetName() == c.component && component.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_PRICED {
						t.Errorf("%s in %s = %v: %s", component.GetName(), r.GetResource(), component.GetPriceStatus(), component.GetPriceDetail())
					}
				}
			}
		})
	}
}
