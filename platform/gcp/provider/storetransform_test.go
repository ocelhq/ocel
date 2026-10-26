package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
)

func shapedStore(t *testing.T, pass transform.Pass, resource provider.Resource, kind string) *costv1.Resource {
	t.Helper()
	p := pushing(t, "")
	p.transforms = pass
	set, err := p.ShapeCost(context.Background(), provider.ShapeRequest{
		Deploy:    provider.DeploySpec{Slug: "shop", Tier: environment.TierProduction, Env: "prod"},
		Resources: []provider.Resource{resource},
	})
	if err != nil {
		t.Fatalf("ShapeCost() = %v", err)
	}
	for _, shaped := range set.GetResources() {
		if shaped.GetType() == kind && strings.Contains(shaped.GetName(), resource.Name) {
			return shaped
		}
	}
	t.Fatalf("ShapeCost() shaped %v, want the %s behind %s", set.GetResources(), kind, resource.Name)
	return nil
}

func aDatabaseResource() provider.Resource {
	return provider.Resource{Name: "orders", Declared: "orders", Type: provider.BindingPostgres, Postgres: &provider.PostgresSpec{Version: "17"}}
}

func TestADatabaseIsPricedAtTheMachineAndDiskItsTransformSets(t *testing.T) {
	shaped := shapedStore(t, &patching{patches: transform.Patches{"instance": {"settings": map[string]any{
		"tier": "db-custom-4-15360", "availabilityType": "ZONAL", "diskSize": 100,
	}}}}, aDatabaseResource(), "google_sql_database_instance")

	settings := shaped.GetProperties().AsMap()["settings"].(map[string]any)
	if settings["tier"] != "db-custom-4-15360" || settings["availability_type"] != "ZONAL" || settings["disk_size"] != float64(100) {
		t.Errorf("the database is priced as %v, want the machine, availability and disk its transform sets", settings)
	}
}

func TestADatabaseWhoseMachineABindingFillsIsPricedAsUnknown(t *testing.T) {
	shaped := shapedStore(t, &patching{patches: transform.Patches{"instance": {"settings": map[string]any{
		"tier": map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "sizes", "property": "tier"}},
		"ipConfiguration": map[string]any{"pscConfigs": []any{map[string]any{"pscAutoConnections": []any{map[string]any{
			"consumerNetwork": map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "network", "property": "id"}},
		}}}}},
	}}}}, aDatabaseResource(), "google_sql_database_instance")

	if !slices.Contains(shaped.GetUnknown(), "settings.tier") {
		t.Errorf("the database is priced with unknown %v, want its machine type unknown until the deploy reads the record", shaped.GetUnknown())
	}
}

func TestAStoreIsPricedAtTheReplicasItsTransformSets(t *testing.T) {
	store := provider.Resource{Name: "cache", Declared: "cache", Type: provider.BindingKV, KV: &provider.KVSpec{MemoryBytes: 256 << 20}}

	shaped := shapedStore(t, &patching{patches: transform.Patches{"instance": {"replicaCount": 3}}}, store, "google_memorystore_instance")
	if replicas := shaped.GetProperties().AsMap()["replica_count"]; replicas != float64(3) {
		t.Errorf("the store is priced at %v replicas, want the three its transform sets", replicas)
	}

	shaped = shapedStore(t, &patching{patches: transform.Patches{"instance": {
		"replicaCount":                map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "sizes", "property": "replicas"}},
		"desiredAutoCreatedEndpoints": []any{map[string]any{"network": map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "network", "property": "id"}}}},
	}}}, store, "google_memorystore_instance")
	if !slices.Contains(shaped.GetUnknown(), "replica_count") {
		t.Errorf("the store is priced with unknown %v, want its replicas unknown until the deploy reads the record", shaped.GetUnknown())
	}
}

func TestAStoreWhoseBackupRetentionABindingFillsIsPricedRatherThanRefused(t *testing.T) {
	store := provider.Resource{Name: "cache", Declared: "cache", Type: provider.BindingKV, KV: &provider.KVSpec{MemoryBytes: 256 << 20}}

	shapedStore(t, &patching{patches: transform.Patches{"instance": {"automatedBackupConfig": map[string]any{
		"retention":              map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "backups", "property": "retention"}},
		"fixedFrequencySchedule": map[string]any{"startTime": map[string]any{"hours": 3}},
	}}}}, store, "google_memorystore_instance")
}
