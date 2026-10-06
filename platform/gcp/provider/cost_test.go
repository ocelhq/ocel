package gcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestShapeOfAPreviewBehindTheLoadBalancerIncludesTheWildcardCertificate(t *testing.T) {
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
	if counts["google_certificate_manager_certificate_map_entry"] != 2 || counts["google_compute_region_network_endpoint_group"] != 1 {
		t.Errorf("counts = %v, want the preview wildcard's certificate beside the hostname's, and the hostname's endpoint group alone", counts)
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

func topicsManifest() *contractv1.Manifest {
	manifest := shopManifest()
	manifest.Workers = []*contractv1.ManifestWorker{{Name: "media", App: "web", Compute: string(provider.ComputeServerless), Concurrency: 4}}
	manifest.Resources = append(manifest.Resources,
		&contractv1.ManifestResource{
			LogicalName: "task--resize",
			Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, Name: "resize"},
			Config: &contractv1.ManifestResource_Topic{Topic: &contractv1.ManifestTopic{
				Cron:      "*/5 * * * *",
				Consumers: []*contractv1.ManifestConsumer{{Name: "resize", Worker: "media", Exclusive: true}},
			}},
		},
		&contractv1.ManifestResource{
			LogicalName: "topic--orders",
			Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC, Name: "orders"},
			Config: &contractv1.ManifestResource_Topic{Topic: &contractv1.ManifestTopic{
				Consumers: []*contractv1.ManifestConsumer{{Name: "ship", Worker: "media"}, {Name: "bill", Worker: "media"}},
			}},
		},
	)
	return manifest
}

func TestShapeOfTopicsAndTasksIsTheirPubSubTopologyTheTiersQueueAndDatabaseAndEachWorker(t *testing.T) {
	client, costs := costServed(t)

	set, err := client.Shape(context.Background(), &contractv1.ShapeRequest{
		Manifest:    topicsManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Shape() = %v", err)
	}
	counts := typeCounts(set)
	for typ, want := range map[string]int{
		"google_pubsub_topic":         5,
		"google_pubsub_subscription":  3,
		"google_cloud_tasks_queue":    1,
		"google_firestore_database":   3,
		"google_cloud_run_v2_service": 4,
		"google_cloud_scheduler_job":  2,
	} {
		if counts[typ] != want {
			t.Errorf("the shape lists %d %s, want %d: %v", counts[typ], typ, want, counts)
		}
	}
	for _, r := range set.GetResources() {
		switch {
		case r.GetType() == "google_pubsub_topic" || r.GetType() == "google_pubsub_subscription":
			if r.GetScope() != "project:shop/environment:prod" {
				t.Errorf("%s is shaped under %s, want the environment that declares it", r.GetName(), r.GetScope())
			}
		case r.GetType() == "google_cloud_tasks_queue":
			if r.GetScope() != "project:shop/shared:production" {
				t.Errorf("the delay queue is shaped under %s, want the tier it is bootstrapped for", r.GetScope())
			}
		case r.GetType() == "google_cloud_run_v2_service" && r.GetScope() == "project:shop/environment:prod/app:web" && r.GetProperties().AsMap()["ingress"] == "INGRESS_TRAFFIC_INTERNAL_ONLY":
			template := r.GetProperties().AsMap()["template"].(map[string]any)
			if template["scaling"].(map[string]any)["min_instance_count"] != float64(0) {
				t.Errorf("the worker %s keeps instances warm, want it scaled from zero", r.GetName())
			}
		}
	}

	est, err := costs.Price(context.Background(), &costv1.PriceRequest{Resources: set, Usage: &costv1.Usage{Profile: costv1.Profile_PROFILE_HEAVY}})
	if err != nil {
		t.Fatalf("Price() = %v", err)
	}
	for _, typ := range []string{"google_pubsub_topic", "google_pubsub_subscription"} {
		if r := estimateOfType(t, est, set, typ, "project:shop/environment:prod"); r.GetStatus() != costv1.ResourceEstimate_STATUS_PRICED {
			t.Errorf("a %s is priced %s, want priced by its throughput", typ, r.GetStatus())
		}
	}
	statuses := map[costv1.ResourceEstimate_Status]int{}
	for _, resource := range set.GetResources() {
		if resource.GetType() != "google_pubsub_topic" {
			continue
		}
		for _, r := range est.GetResources() {
			if r.GetResource() == resource.GetId() {
				statuses[r.GetStatus()]++
			}
		}
	}
	if want := map[costv1.ResourceEstimate_Status]int{costv1.ResourceEstimate_STATUS_PRICED: 2, costv1.ResourceEstimate_STATUS_FREE: 3}; !maps.Equal(statuses, want) {
		t.Errorf("the topics are priced %v, want the two live topics priced by throughput and the three dead-letter topics free", statuses)
	}
	if r := estimateOfType(t, est, set, "google_cloud_tasks_queue", "project:shop/shared:production"); r.GetStatus() != costv1.ResourceEstimate_STATUS_PRICED {
		t.Errorf("the delay queue is priced %s, want priced by its operations", r.GetStatus())
	}
}

func nextManifest(container bool) *contractv1.Manifest {
	app := &contractv1.ManifestApp{Name: "web", Framework: &contractv1.Framework{Name: "next"}}
	if container {
		app.Artifact = &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{
			Image: "europe-west1-docker.pkg.dev/acme-prod/ocel/web@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", HealthCheckPath: "/healthz",
			MinInstances: 1, MaxInstances: 1,
		}}
	} else {
		app.Artifact = &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{Functions: []*contractv1.ManifestFunction{
			{LogicalName: "fn--web--entry", Framework: &contractv1.Framework{Name: "next"}},
		}}}
	}
	return &contractv1.Manifest{Slug: "shop", Apps: []*contractv1.ManifestApp{app}}
}

func TestShapeOfANextAppBilledPerRequestListsTheTiersQueueAndRefreshAccount(t *testing.T) {
	client, _ := costServed(t)
	shaped := func(container bool) *costv1.ResourceSet {
		set, err := client.Shape(context.Background(), &contractv1.ShapeRequest{
			Manifest:    nextManifest(container),
			Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		})
		if err != nil {
			t.Fatalf("Shape() = %v", err)
		}
		return set
	}

	perRequest, container := shaped(false), shaped(true)

	counts, without := typeCounts(perRequest), typeCounts(container)
	if counts["google_cloud_tasks_queue"] != 1 {
		t.Errorf("counts = %v, want the tier's delay queue", counts)
	}
	if counts["google_service_account"] != without["google_service_account"]+1 {
		t.Errorf("counts = %v, want the refresh account beside the %d accounts of an app that refreshes on its own", counts, without["google_service_account"])
	}
	if counts["google_firestore_database"] != without["google_firestore_database"] {
		t.Errorf("counts = %v, want no task database: a refresh keeps no run records", counts)
	}
	for _, r := range perRequest.GetResources() {
		if r.GetType() == "google_cloud_tasks_queue" && r.GetScope() != "project:shop/shared:production" {
			t.Errorf("the queue is scoped %q, want the tier it is shared by", r.GetScope())
		}
	}
}

func TestShapeOfANextAppOnContainerComputeListsNoTaskQueue(t *testing.T) {
	client, _ := costServed(t)

	set, err := client.Shape(context.Background(), &contractv1.ShapeRequest{
		Manifest:    nextManifest(true),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Shape() = %v", err)
	}

	if counts := typeCounts(set); counts["google_cloud_tasks_queue"] != 0 {
		t.Errorf("counts = %v, want no queue for an app that refreshes on its own", counts)
	}
}

func TestShapeOfANextPreviewBehindIdentityAwareProxyListsNoRefreshQueueAndKeepsItsCPU(t *testing.T) {
	client, _ := costServed(t)
	shaped := func(container bool) *costv1.ResourceSet {
		set, err := client.Shape(context.Background(), &contractv1.ShapeRequest{
			Manifest:    nextManifest(container),
			Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-42"},
		})
		if err != nil {
			t.Fatalf("Shape() = %v", err)
		}
		return set
	}

	set, container := shaped(false), shaped(true)

	counts, without := typeCounts(set), typeCounts(container)
	if counts["google_cloud_tasks_queue"] != 0 {
		t.Errorf("counts = %v, want no delay queue: a gated preview refreshes on its own", counts)
	}
	if counts["google_service_account"] != without["google_service_account"] {
		t.Errorf("counts = %v, want no refresh account beside the %d accounts of an app that refreshes on its own", counts, without["google_service_account"])
	}
	for _, r := range set.GetResources() {
		if r.GetType() != "google_cloud_run_v2_service" || !strings.HasSuffix(r.GetScope(), "/app:web") {
			continue
		}
		template := r.GetProperties().AsMap()["template"].(map[string]any)
		resources := template["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)
		if resources["cpu_idle"] != false {
			t.Errorf("%s has cpu_idle = %v, want false: a gated Next preview is billed per instance", r.GetName(), resources["cpu_idle"])
		}
	}
}

func TestANextAppIsPricedAtTheMemoryItsServicesRunWith(t *testing.T) {
	client, _ := costServed(t)
	image := "europe-west1-docker.pkg.dev/acme-prod/ocel/web@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	serverless := func(name, framework string) *contractv1.ManifestApp {
		return &contractv1.ManifestApp{Name: name, Framework: &contractv1.Framework{Name: framework},
			Artifact: &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{Functions: []*contractv1.ManifestFunction{
				{LogicalName: "fn--" + name + "--entry", Framework: &contractv1.Framework{Name: framework}},
			}}}}
	}
	manifest := &contractv1.Manifest{
		Slug: "shop",
		Apps: []*contractv1.ManifestApp{
			serverless("site", "next"),
			serverless("api", "node"),
			{Name: "docs", Framework: &contractv1.Framework{Name: "next"},
				Artifact: &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{Image: image, HealthCheckPath: "/", MinInstances: 1, MaxInstances: 1}}},
		},
	}

	set, err := client.Shape(context.Background(), &contractv1.ShapeRequest{
		Manifest:    manifest,
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Shape() = %v", err)
	}

	want := map[string]string{
		"project:shop/environment:prod/app:site": "2048Mi",
		"project:shop/environment:prod/app:docs": "2048Mi",
		"project:shop/environment:prod/app:api":  "512Mi",
	}
	seen := map[string]bool{}
	for _, r := range set.GetResources() {
		memory, priced := want[r.GetScope()]
		if r.GetType() != "google_cloud_run_v2_service" || !priced {
			continue
		}
		seen[r.GetScope()] = true
		template := r.GetProperties().AsMap()["template"].(map[string]any)
		limits := template["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)["limits"].(map[string]any)
		if limits["memory"] != memory {
			t.Errorf("%s is priced at %v of memory, want the %s its services run with", r.GetScope(), limits["memory"], memory)
		}
	}
	if len(seen) != len(want) {
		t.Errorf("shaped the services of %v, want one for each of %v", seen, want)
	}
}
