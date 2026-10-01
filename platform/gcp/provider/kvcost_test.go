package gcp_test

import (
	"context"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

func storeManifest(memory string) *contractv1.Manifest {
	manifest := shopManifest()
	manifest.Resources = append(manifest.Resources, &contractv1.ManifestResource{
		LogicalName: "kv--cache",
		Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_KV, Name: "cache"},
		Config:      &contractv1.ManifestResource_Kv{Kv: &resourcesv1.KvConfig{Memory: memory}},
	})
	return manifest
}

func shapeOfAStore(t *testing.T, region, memory string) (*costv1.ResourceSet, *costv1.Estimate) {
	t.Helper()
	client, costs := costServedIn(t, region)
	set, err := client.Shape(context.Background(), &contractv1.ShapeRequest{
		Manifest:    storeManifest(memory),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Shape() = %v", err)
	}
	est, err := costs.Price(context.Background(), &costv1.PriceRequest{Resources: set})
	if err != nil {
		t.Fatalf("Price() = %v", err)
	}
	return set, est
}

func resourceOfType(t *testing.T, set *costv1.ResourceSet, typ string) *costv1.Resource {
	t.Helper()
	for _, resource := range set.GetResources() {
		if resource.GetType() == typ {
			return resource
		}
	}
	t.Fatalf("the shape lists no %s", typ)
	return nil
}

func TestShapeOfADeclaredStoreIsOneMemorystoreNodeOnTheTiersNetwork(t *testing.T) {
	set, _ := shapeOfAStore(t, "us-central1", "2gb")

	instance := resourceOfType(t, set, "google_memorystore_instance")
	if instance.GetScope() != "project:shop/environment:prod" {
		t.Errorf("the store is shaped under %s, want the environment that declares it", instance.GetScope())
	}
	properties := instance.GetProperties().AsMap()
	if properties["node_type"] != "custom-mini" || properties["replica_count"] != float64(0) ||
		properties["persistence_config"].(map[string]any)["mode"] != "AOF" {
		t.Errorf("the store is shaped as %v, want one custom-mini node with append-only persistence", properties)
	}
	for _, typ := range []string{"google_compute_network", "google_compute_subnetwork", "google_network_connectivity_service_connection_policy"} {
		if scope := resourceOfType(t, set, typ).GetScope(); scope != "project:shop/shared:production" {
			t.Errorf("the %s is shaped under %s, want the tier it is bootstrapped for", typ, scope)
		}
	}
}

func TestPriceOfAStoreBillsItsNodeAndItsAppendOnlyFileEveryHour(t *testing.T) {
	set, est := shapeOfAStore(t, "us-central1", "256mb")

	store := estimateOfType(t, est, set, "google_memorystore_instance", "project:shop/environment:prod")
	if got := componentNamed(t, est, store.GetResource(), "Node").GetMonthlyCost(); got != "22.48" {
		t.Errorf("the node costs %s, want 22.48 (730 h of custom-pico at 0.0308)", got)
	}
	if got := componentNamed(t, est, store.GetResource(), "Append-only persistence").GetMonthlyCost(); got != "0.50" {
		t.Errorf("persistence costs %s, want 0.50 (1.25 GB for 730 h at 0.00054795)", got)
	}
	if store.GetMonthlyUsage() != "0.00" {
		t.Errorf("the store bills %s with usage, want none: a node bills by the hour whatever it serves", store.GetMonthlyUsage())
	}
	for _, typ := range []string{"google_compute_network", "google_compute_subnetwork", "google_network_connectivity_service_connection_policy"} {
		if got := estimateOfType(t, est, set, typ, "project:shop/shared:production").GetStatus(); got != costv1.ResourceEstimate_STATUS_FREE {
			t.Errorf("the %s is priced %s, want free", typ, got)
		}
	}
}
