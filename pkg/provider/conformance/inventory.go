package conformance

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/pricing/ruleset"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

const inventoryImageDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func runInventory(t *testing.T, suite Suite) {
	t.Helper()
	server := httptest.NewServer(providerserver.ConformanceMux(suite.Server))
	t.Cleanup(server.Close)

	providerClient := client(server.Client(), server.URL)
	configured, err := providerClient.Configure(context.Background(), configureWith(t, suite.Options))
	if err != nil {
		t.Fatalf("Configure() error = %v, want the session configured", err)
	}
	if !configured.GetFacts().GetTakesInventory() {
		_, err := providerClient.Inventory(context.Background(), &contractv1.InventoryRequest{
			Manifest:    InventoryManifest(),
			Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		})
		if connect.CodeOf(err) != connect.CodeUnimplemented {
			t.Fatalf("Inventory() error = %v from a provider that says it takes no inventory, want Unimplemented", err)
		}
		t.Skip("this provider takes no inventory, and says so")
	}
	vendor := string(readDeclaredFacts(t, suite).Vendor)
	RunInventory(t, providerClient, vendor, suite.Rules)
}

func InventoryManifest() *contractv1.Manifest {
	return &contractv1.Manifest{
		Slug: "conformance",
		Apps: []*contractv1.ManifestApp{
			{Name: "web", Framework: &contractv1.Framework{Name: "node"},
				Domains: []*contractv1.TierDomains{{Tier: environmentv1.Tier_TIER_PRODUCTION, Hostnames: []string{"web.example.com"}}},
				Artifact: &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{Functions: []*contractv1.ManifestFunction{
					{LogicalName: "fn--web--entry", Framework: &contractv1.Framework{Name: "node"}},
				}}}},
			{Name: "api", Framework: &contractv1.Framework{Name: "go"},
				Artifact: &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{
					Image: "registry.example.com/conformance/api@sha256:" + inventoryImageDigest, HealthCheckPath: "/healthz", MinInstances: 1, MaxInstances: 1,
				}}},
		},
		Resources: []*contractv1.ManifestResource{
			{LogicalName: "main", Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "main"}, Config: &contractv1.ManifestResource_Postgres{Postgres: &resourcesv1.PostgresConfig{}}},
			{LogicalName: "uploads", Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, Name: "uploads"}, Config: &contractv1.ManifestResource_Bucket{Bucket: &resourcesv1.BucketConfig{}}},
		},
	}
}

func RunInventory(t *testing.T, client contractv1connect.ProviderServiceClient, vendor string, rules pricing.Table) {
	t.Helper()

	set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest:    InventoryManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Inventory() error = %v from a provider that says it takes inventory, want the resources a deploy would create", err)
	}
	inventoryIsConsistent(t, set, vendor)
	if unpriced := unpricedTypes(set, rules); len(unpriced) > 0 {
		t.Errorf("the inventory lists %v, which no pricing rule names, so a scan prices them at nothing", unpriced)
	}
}

func unpricedTypes(set *costv1.ResourceSet, rules pricing.Table) []string {
	if rules == nil {
		rules = ruleset.Table()
	}
	var unpriced []string
	for _, resource := range set.GetResources() {
		if _, priced := rules[resource.GetType()]; !priced && !slices.Contains(unpriced, resource.GetType()) {
			unpriced = append(unpriced, resource.GetType())
		}
	}
	return unpriced
}

func inventoryIsConsistent(t *testing.T, set *costv1.ResourceSet, vendor string) {
	t.Helper()
	if set.GetSource() != provider.InventorySource {
		t.Errorf("source = %q, want %q", set.GetSource(), provider.InventorySource)
	}
	scopes := map[string]*costv1.Scope{}
	for _, scope := range set.GetScopes() {
		if scope.GetId() == "" || scope.GetKind() == "" {
			t.Errorf("scope %v has no id or kind", scope)
		}
		if _, dup := scopes[scope.GetId()]; dup {
			t.Errorf("scope %s appears twice", scope.GetId())
		}
		scopes[scope.GetId()] = scope
	}
	for _, scope := range set.GetScopes() {
		if parent := scope.GetParent(); parent != "" {
			if _, ok := scopes[parent]; !ok {
				t.Errorf("scope %s hangs on %s, which the tree does not contain", scope.GetId(), parent)
			}
		}
	}
	ids := map[string]bool{}
	for _, resource := range set.GetResources() {
		if resource.GetId() == "" || ids[resource.GetId()] {
			t.Errorf("resource %v has no id or repeats one", resource)
		}
		ids[resource.GetId()] = true
		if _, ok := scopes[resource.GetScope()]; !ok {
			t.Errorf("resource %s sits in scope %q, which the tree does not contain", resource.GetId(), resource.GetScope())
		}
		if resource.GetVendor() == "" || resource.GetType() == "" {
			t.Errorf("resource %s has no vendor or type", resource.GetId())
		}
		if vendor != "" && resource.GetVendor() == vendor && resource.GetRegion() == "" {
			t.Errorf("resource %s is this provider's own and names no region, so its price cannot be looked up", resource.GetId())
		}
		for _, unknown := range resource.GetUnknown() {
			if _, present := resource.GetProperties().GetFields()[unknown]; present {
				t.Errorf("resource %s lists %s as unknown and has a value for it", resource.GetId(), unknown)
			}
		}
	}
	if len(set.GetResources()) == 0 {
		t.Error("the inventory lists no resources for a deploy with two apps and two declared resources")
	}
}
