package cloudflare_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func inventorySet(t *testing.T, tier environment.Tier) *costv1.ResourceSet {
	t.Helper()
	inventory, err := cloudflare.Inventory(pricing.EdgeSite{Slug: "shop", Tier: tier})
	if err != nil {
		t.Fatal(err)
	}
	tree := &pricing.Tree{}
	tree.AddEdge(pricing.EdgeScopes{
		Shared:      tree.Scope("", pricing.ScopeShared, string(tier)),
		Environment: tree.Scope("", pricing.ScopeEnvironment, "prod"),
	}, inventory)
	set, err := tree.Set("ocel")
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestTheEdgeInventoryListsItsPlanStoreAndEntry(t *testing.T) {
	t.Parallel()

	set := inventorySet(t, environment.TierProduction)
	counts := map[string]int{}
	for _, r := range set.GetResources() {
		counts[r.GetType()]++
		if r.GetVendor() != cloudflare.Vendor {
			t.Errorf("%s is %s", r.GetId(), r.GetVendor())
		}
	}
	if counts[cloudflare.TypeAccountSubscription] != 1 || counts[cloudflare.TypeWorkersScript] != 2 || len(counts) != 2 {
		t.Errorf("counts = %v, want the plan and the store and entry workers, and no bucket", counts)
	}
	preview := inventorySet(t, environment.TierPreview)
	if n := len(preview.GetResources()); n != len(set.GetResources())+1 {
		t.Errorf("a preview tier lists %d resources, want the production set plus the shared preview entry worker", n)
	}
}

func TestEveryEntryWorkerListsItsPurgeQueueAsASQLiteDurableObjectClass(t *testing.T) {
	t.Parallel()

	set := inventorySet(t, environment.TierPreview)
	for _, scope := range []string{"environment:prod", "shared:preview"} {
		var entry *costv1.Resource
		for _, resource := range set.GetResources() {
			if resource.GetType() == cloudflare.TypeWorkersScript && resource.GetScope() == scope && strings.Contains(resource.GetName(), "entry") {
				entry = resource
			}
		}
		if entry == nil {
			t.Fatalf("no entry worker under %s", scope)
		}
		if classes := sqliteClasses(entry); !slices.Contains(classes, "PurgeQueue") {
			t.Errorf("%s creates Durable Object classes %v, want its PurgeQueue", entry.GetName(), classes)
		}
	}
}

func TestTheReleasesStoreListsItsDurableObjectNamespaceBinding(t *testing.T) {
	t.Parallel()

	for _, resource := range inventorySet(t, environment.TierProduction).GetResources() {
		if resource.GetType() != cloudflare.TypeWorkersScript || strings.Contains(resource.GetName(), "entry") {
			continue
		}
		properties := resource.GetProperties().AsMap()
		bindings, _ := properties["bindings"].([]any)
		bound := slices.ContainsFunc(bindings, func(binding any) bool {
			fields, _ := binding.(map[string]any)
			return fields["type"] == "durable_object_namespace" && fields["class_name"] == "ReleasesStore"
		})
		if !bound || !slices.Contains(sqliteClasses(resource), "ReleasesStore") {
			t.Errorf("%s is listed as %v, want a durable_object_namespace binding to ReleasesStore and the migration that creates it", resource.GetName(), properties)
		}
		return
	}
	t.Fatal("no releases store script")
}

func TestEveryWorkersScriptListsOnlyTerraformArguments(t *testing.T) {
	t.Parallel()

	arguments := []string{"bindings", "migrations"}
	for _, resource := range inventorySet(t, environment.TierPreview).GetResources() {
		if resource.GetType() != cloudflare.TypeWorkersScript {
			continue
		}
		for key := range resource.GetProperties().GetFields() {
			if !slices.Contains(arguments, key) {
				t.Errorf("%s lists %q, which is no argument of cloudflare_workers_script", resource.GetName(), key)
			}
		}
	}
}

func sqliteClasses(script *costv1.Resource) []string {
	migrations, _ := script.GetProperties().AsMap()["migrations"].(map[string]any)
	created, _ := migrations["new_sqlite_classes"].([]any)
	var classes []string
	for _, class := range created {
		name, _ := class.(string)
		classes = append(classes, name)
	}
	return classes
}
