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
	"github.com/ocelhq/ocel/pkg/providerkit"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func costServed(t *testing.T) (contractv1connect.ProviderServiceClient, costv1connect.CostServiceClient) {
	t.Helper()
	p := newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"})
	spec := providerkit.Spec{
		Version: "test",
		New:     func(context.Context, providerkit.Settings) (providerkit.Provider, error) { return p, nil },
	}
	server := httptest.NewServer(providerkit.ConformanceMux(spec))
	t.Cleanup(server.Close)
	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	return client, costv1connect.NewCostServiceClient(server.Client(), server.URL)
}

func shopManifest() *contractv1.Manifest {
	return &contractv1.Manifest{
		SchemaVersion: "provider.v1",
		Slug:          "shop",
		Apps: []*contractv1.ManifestApp{
			{Name: "web", Runtime: &contractv1.Runtime{Name: "node"}, Compute: "serverless",
				Domains: []*contractv1.TierDomains{{Tier: environmentv1.Tier_TIER_PRODUCTION, Hostnames: []string{"shop.example.com"}}}},
			{Name: "api", Runtime: &contractv1.Runtime{Name: "go"}, Compute: "container"},
		},
		Functions: []*contractv1.ManifestFunction{
			{LogicalName: "fn--web--entry", App: "web", Runtime: &contractv1.Runtime{Name: "node"}},
		},
		Containers: []*contractv1.ManifestContainer{
			{App: "api", Image: "europe-west1-docker.pkg.dev/acme-prod/ocel/api@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", HealthCheckPath: "/healthz"},
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
	if counts["google_cloud_run_v2_service"] != 3 || counts["google_storage_bucket"] != 2 || counts["google_compute_global_forwarding_rule"] != 0 {
		t.Errorf("counts = %v, want the two apps' services and the env syncer's, the artifact and state buckets, and no load balancer", counts)
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

func TestShapeOfAPreviewBehindTheLoadBalancerCarriesTheWildcard(t *testing.T) {
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
