package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
)

func listedStore(t *testing.T, pass transform.Pass, resource provider.Resource, kind string) *costv1.Resource {
	t.Helper()
	p := pushing(t, "")
	p.transforms = pass
	set, err := p.Inventory(context.Background(), provider.InventoryRequest{
		Deploy:    provider.DeploySpec{Slug: "shop", Tier: environment.TierProduction, Env: "prod"},
		Resources: []provider.Resource{resource},
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}
	for _, listed := range set.GetResources() {
		if listed.GetType() == kind && strings.Contains(listed.GetName(), resource.Name) {
			return listed
		}
	}
	t.Fatalf("Inventory() lists %v, want the %s behind %s", set.GetResources(), kind, resource.Name)
	return nil
}

func aDatabaseResource() provider.Resource {
	return provider.Resource{Name: "orders", Declared: "orders", Type: provider.BindingPostgres, Postgres: &provider.PostgresSpec{Version: "17"}}
}

func TestADatabaseIsListedAtTheMachineAndDiskItsTransformSets(t *testing.T) {
	listed := listedStore(t, &patching{patches: transform.Patches{"instance": {"settings": map[string]any{
		"tier": "db-custom-4-15360", "availabilityType": "ZONAL", "diskSize": 100,
	}}}}, aDatabaseResource(), "google_sql_database_instance")

	settings := listed.GetProperties().AsMap()["settings"].(map[string]any)
	if settings["tier"] != "db-custom-4-15360" || settings["availability_type"] != "ZONAL" || settings["disk_size"] != float64(100) {
		t.Errorf("the database is listed as %v, want the machine, availability and disk its transform sets", settings)
	}
}

func TestADatabaseWhoseMachineABindingFillsIsListedAsUnknown(t *testing.T) {
	listed := listedStore(t, &patching{patches: transform.Patches{"instance": {"settings": map[string]any{
		"tier": map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "sizes", "property": "tier"}},
		"ipConfiguration": map[string]any{"pscConfigs": []any{map[string]any{"pscAutoConnections": []any{map[string]any{
			"consumerNetwork": map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "network", "property": "id"}},
		}}}}},
	}}}}, aDatabaseResource(), "google_sql_database_instance")

	if !slices.Contains(listed.GetUnknown(), "settings.tier") {
		t.Errorf("the database is listed with unknown %v, want its machine type unknown until the deploy reads the record", listed.GetUnknown())
	}
}

func TestAStoreIsListedAtTheReplicasItsTransformSets(t *testing.T) {
	store := provider.Resource{Name: "cache", Declared: "cache", Type: provider.BindingKV, KV: &provider.KVSpec{MemoryBytes: 256 << 20}}

	listed := listedStore(t, &patching{patches: transform.Patches{"instance": {"replicaCount": 3}}}, store, "google_memorystore_instance")
	if replicas := listed.GetProperties().AsMap()["replica_count"]; replicas != float64(3) {
		t.Errorf("the store is listed at %v replicas, want the three its transform sets", replicas)
	}

	listed = listedStore(t, &patching{patches: transform.Patches{"instance": {
		"replicaCount":                map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "sizes", "property": "replicas"}},
		"desiredAutoCreatedEndpoints": []any{map[string]any{"network": map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "network", "property": "id"}}}},
	}}}, store, "google_memorystore_instance")
	if !slices.Contains(listed.GetUnknown(), "replica_count") {
		t.Errorf("the store is listed with unknown %v, want its replicas unknown until the deploy reads the record", listed.GetUnknown())
	}
}

func TestAStoreWhoseBackupRetentionABindingFillsIsListedRatherThanRefused(t *testing.T) {
	store := provider.Resource{Name: "cache", Declared: "cache", Type: provider.BindingKV, KV: &provider.KVSpec{MemoryBytes: 256 << 20}}

	listedStore(t, &patching{patches: transform.Patches{"instance": {"automatedBackupConfig": map[string]any{
		"retention":              map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "backups", "property": "retention"}},
		"fixedFrequencySchedule": map[string]any{"startTime": map[string]any{"hours": 3}},
	}}}}, store, "google_memorystore_instance")
}
