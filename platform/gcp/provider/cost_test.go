package gcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/cost/v1/costv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func costServed(t *testing.T) (contractv1connect.ProviderServiceClient, costv1connect.CostServiceClient) {
	t.Helper()
	return costServedIn(t, "europe-west1")
}

func costServedIn(t *testing.T, region string) (contractv1connect.ProviderServiceClient, costv1connect.CostServiceClient) {
	t.Helper()
	p := newProvider(t, gcp.Options{Project: "acme-prod", Region: region})
	config := providerserver.Config{
		Version: "test",
		New:     func(context.Context, provider.Settings) (provider.Provider, error) { return p, nil },
	}
	server := httptest.NewServer(providerserver.ConformanceMux(config))
	t.Cleanup(server.Close)
	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{Config: &contractv1.ProviderConfig{ProjectDir: t.TempDir()}}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	return client, costv1connect.NewCostServiceClient(server.Client(), server.URL)
}

func shopManifest() *contractv1.Manifest {
	return &contractv1.Manifest{
		Slug: "shop",
		Apps: []*contractv1.ManifestApp{
			{Name: "web", Framework: &contractv1.Framework{Name: "node"},
				Domains: []*contractv1.TierDomains{{Tier: environmentv1.Tier_TIER_PRODUCTION, Hostnames: []string{"shop.example.com"}}},
				Artifact: &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{Functions: []*contractv1.ManifestFunction{
					{LogicalName: "fn--web--entry", Framework: &contractv1.Framework{Name: "node"}},
				}}}},
			{Name: "api", Framework: &contractv1.Framework{Name: "go"},
				Artifact: &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{
					Image: "europe-west1-docker.pkg.dev/acme-prod/ocel/api@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", HealthCheckPath: "/healthz",
					MinInstances: 1, MaxInstances: 1,
				}}},
		},
		Resources: []*contractv1.ManifestResource{
			{LogicalName: "uploads", Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, Name: "uploads"}, Config: &contractv1.ManifestResource_Bucket{Bucket: &resourcesv1.BucketConfig{}}},
		},
	}
}

func golden(t *testing.T, name string, msg proto.Message) {
	t.Helper()
	raw, err := protojson.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if err := json.Indent(&got, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	got.WriteByte('\n')
	path := filepath.Join("testdata", name+".golden.json")
	if *update {
		if err := os.WriteFile(path, got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if !bytes.Equal(want, got.Bytes()) {
		t.Errorf("%s differs from the golden file; run with -update after checking the diff:\n%s", name, got.String())
	}
}

func typeCounts(set *costv1.ResourceSet) map[string]int {
	counts := map[string]int{}
	for _, r := range set.GetResources() {
		counts[r.GetType()]++
	}
	return counts
}

func TestShapeDescribesAProductionDeployServedDirect(t *testing.T) {
	client, _ := costServed(t)

	set, err := client.Shape(context.Background(), &contractv1.ShapeRequest{
		Manifest:    shopManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Shape() = %v", err)
	}
	golden(t, "shape_production_direct", set)

	counts := typeCounts(set)
	if counts["google_cloud_run_v2_service"] != 3 || counts["google_cloud_scheduler_job"] != 1 || counts["google_storage_bucket"] != 2 || counts["google_compute_global_forwarding_rule"] != 0 {
		t.Errorf("counts = %v, want the two apps' services and the env source sync's with its schedule, the artifact and state buckets, and no load balancer", counts)
	}
	for _, r := range set.GetResources() {
		if r.GetVendor() != "gcp" || r.GetRegion() != "europe-west1" {
			t.Errorf("%s is %s in %q", r.GetId(), r.GetVendor(), r.GetRegion())
		}
		if r.GetType() != "google_cloud_run_v2_service" {
			continue
		}
		template := r.GetProperties().AsMap()["template"].(map[string]any)
		min := template["scaling"].(map[string]any)["min_instance_count"].(float64)
		if (r.GetScope() == "project:shop/environment:prod/app:api") != (min == 1) {
			t.Errorf("%s keeps %v instances warm; a container app keeps one and a serverless app none", r.GetName(), min)
		}
	}
}

func TestShapeBehindTheLoadBalancerFrontsEveryHostname(t *testing.T) {
	client, _ := costServed(t)

	set, err := client.Shape(context.Background(), &contractv1.ShapeRequest{
		Manifest:    shopManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		Edge:        &contractv1.EdgeSelection{Kind: string(alb.Kind)},
	})
	if err != nil {
		t.Fatalf("Shape() = %v", err)
	}
	golden(t, "shape_production_alb", set)

	counts := typeCounts(set)
	if counts["google_compute_global_forwarding_rule"] != 1 || counts["google_compute_backend_service"] != 2 || counts["google_compute_region_network_endpoint_group"] != 1 {
		t.Errorf("counts = %v, want one front, the not-found backend and one CDN backend for the one hostname", counts)
	}
}

func TestShapeOfAPreviewBehindTheLoadBalancerIncludesTheWildcard(t *testing.T) {
	client, _ := costServed(t)

	set, err := client.Shape(context.Background(), &contractv1.ShapeRequest{
		Manifest:    shopManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-42"},
		Edge:        &contractv1.EdgeSelection{Kind: string(alb.Kind)},
	})
	if err != nil {
		t.Fatalf("Shape() = %v", err)
	}
	golden(t, "shape_preview_alb", set)

	counts := typeCounts(set)
	if counts["google_compute_region_network_endpoint_group"] != 2 {
		t.Errorf("counts = %v, want the preview wildcard's endpoint group beside the hostname's", counts)
	}
}

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

func TestPriceOfAStoreBillsItsNodeAndItsAppendOnlyFileEveryHourAndTrafficFromOtherZonesByTheGiB(t *testing.T) {
	set, est := shapeOfAStore(t, "us-central1", "256mb")

	store := estimateOfType(t, est, set, "google_memorystore_instance", "project:shop/environment:prod")
	if got := componentNamed(t, est, store.GetResource(), "Node").GetMonthlyCost(); got != "22.48" {
		t.Errorf("the node costs %s, want 22.48 (730 h of custom-pico at 0.0308)", got)
	}
	if got := componentNamed(t, est, store.GetResource(), "Append-only persistence").GetMonthlyCost(); got != "0.50" {
		t.Errorf("persistence costs %s, want 0.50 (1.25 GB for 730 h at 0.00054795)", got)
	}
	traffic := componentNamed(t, est, store.GetResource(), "Inter-zone data processed")
	if got := traffic.GetMonthlyCost(); got != "0.10" || !traffic.GetUsageBased() {
		t.Errorf("inter-zone traffic costs %s, want 0.10 by usage (10 GiB at Private Service Connect's 0.01)", got)
	}
	if store.GetMonthlyUsage() != "0.10" {
		t.Errorf("the store bills %s with usage, want only its inter-zone traffic", store.GetMonthlyUsage())
	}
	for _, typ := range []string{"google_compute_network", "google_compute_subnetwork", "google_network_connectivity_service_connection_policy"} {
		if got := estimateOfType(t, est, set, typ, "project:shop/shared:production").GetStatus(); got != costv1.ResourceEstimate_STATUS_FREE {
			t.Errorf("the %s is priced %s, want free", typ, got)
		}
	}
}
