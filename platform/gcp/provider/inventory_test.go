package gcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/environment"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func inventoryServed(t *testing.T) contractv1connect.ProviderServiceClient {
	t.Helper()
	return inventoryServedIn(t, "europe-west1")
}

func inventoryServedIn(t *testing.T, region string) contractv1connect.ProviderServiceClient {
	t.Helper()
	return inventoryServedWith(t, gcp.Options{Project: "acme-prod", Region: region})
}

func inventoryServedWith(t *testing.T, options gcp.Options) contractv1connect.ProviderServiceClient {
	t.Helper()
	for _, variable := range []string{"OCEL_FLOCI_GCP_ENDPOINT", "OCEL_FLOCI_FIRESTORE_ENDPOINT"} {
		t.Setenv(variable, "")
	}
	p := newProvider(t, options)
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
	return client
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

func TestInventoryDescribesAProductionDeployServedDirect(t *testing.T) {
	client := inventoryServed(t)

	set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest:    shopManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}
	golden(t, "inventory_production_direct", set)

	counts := typeCounts(set)
	if counts["google_cloud_run_v2_service"] != 3 || counts["google_cloud_scheduler_job"] != 1 || counts["google_storage_bucket"] != 2 || counts["google_compute_global_forwarding_rule"] != 0 ||
		counts["google_kms_key_ring"] != 1 || counts["google_kms_crypto_key"] != 1 || counts["google_service_account"] != 0 {
		t.Errorf("counts = %v, want the two apps' services and the env source sync's with its schedule, the state bucket and the one uploads stores into, "+
			"the key ring with the tier's key alone, which the binding uploads publishes is sealed under, and no load balancer or account", counts)
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

func TestInventoryOfADeclaredBucketIsOneStorageBucketInTheEnvironmentThatDeclaresIt(t *testing.T) {
	client := inventoryServed(t)

	set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest:    shopManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}
	var declared []*costv1.Resource
	for _, r := range set.GetResources() {
		if r.GetType() == "google_storage_bucket" && r.GetScope() == "project:shop/environment:prod" {
			declared = append(declared, r)
		}
	}
	if len(declared) != 1 || declared[0].GetName() != "ocel-app-shop-prod-uploads" {
		t.Fatalf("the environment lists buckets %v, want the one bucket uploads stores into", declared)
	}
	if properties := declared[0].GetProperties().AsMap(); properties["location"] != "europe-west1" || properties["storage_class"] != "STANDARD" {
		t.Errorf("the bucket is listed as %v, want a standard bucket in the deploy's region", properties)
	}
}

func TestInventoryBehindTheLoadBalancerFrontsEveryHostname(t *testing.T) {
	client := inventoryServed(t)

	set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest:    shopManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		Edge:        &contractv1.EdgeSelection{Kind: string(alb.Kind)},
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}
	golden(t, "inventory_production_alb", set)

	counts := typeCounts(set)
	if counts["google_compute_global_forwarding_rule"] != 1 || counts["google_compute_backend_service"] != 2 || counts["google_compute_region_network_endpoint_group"] != 1 {
		t.Errorf("counts = %v, want one front, the not-found backend and one CDN backend for the one hostname", counts)
	}
}

func TestInventoryOfAPreviewBehindTheLoadBalancerIncludesTheWildcardCertificate(t *testing.T) {
	client := inventoryServed(t)

	set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest:    shopManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-42"},
		Edge:        &contractv1.EdgeSelection{Kind: string(alb.Kind)},
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}
	golden(t, "inventory_preview_alb", set)

	counts := typeCounts(set)
	if counts["google_certificate_manager_certificate_map_entry"] != 2 || counts["google_compute_region_network_endpoint_group"] != 1 {
		t.Errorf("counts = %v, want the preview wildcard's certificate beside the hostname's, and the hostname's endpoint group alone", counts)
	}
}

func TestInventoryBehindCloudflareCarriesTheWorkerAndNoLoadBalancer(t *testing.T) {
	client := inventoryServed(t)

	set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest:    shopManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		Edge:        &contractv1.EdgeSelection{Kind: string(cloudflare.Kind)},
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}

	counts := typeCounts(set)
	if counts[cloudflare.TypeWorkersScript] == 0 {
		t.Errorf("counts = %v, want the worker in front of the apps", counts)
	}
	for typ := range counts {
		if strings.HasPrefix(typ, "google_compute_") || strings.HasPrefix(typ, "google_certificate_manager_") {
			t.Errorf("the inventory lists %s, and the worker reaches each release on its revision-tag URL with no load balancer between", typ)
		}
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

func inventoryOfAStore(t *testing.T, environment *environmentv1.Environment, memory string) *costv1.ResourceSet {
	t.Helper()
	client := inventoryServedIn(t, "us-central1")
	set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest:    storeManifest(memory),
		Environment: environment,
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}
	return set
}

func resourceOfType(t *testing.T, set *costv1.ResourceSet, typ string) *costv1.Resource {
	t.Helper()
	for _, resource := range set.GetResources() {
		if resource.GetType() == typ {
			return resource
		}
	}
	t.Fatalf("the inventory lists no %s", typ)
	return nil
}

var productionEnvironment = &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION}

func TestInventoryOfADeclaredStoreIsAMemorystoreNodeWithOneReplicaOnTheTiersNetwork(t *testing.T) {
	set := inventoryOfAStore(t, productionEnvironment, "2gb")

	instance := resourceOfType(t, set, "google_memorystore_instance")
	if instance.GetScope() != "project:shop/environment:prod" {
		t.Errorf("the store is listed under %s, want the environment that declares it", instance.GetScope())
	}
	properties := instance.GetProperties().AsMap()
	if properties["node_type"] != "CUSTOM_MINI" || properties["replica_count"] != float64(1) ||
		properties["persistence_config"].(map[string]any)["mode"] != "AOF" {
		t.Errorf("the store is listed as %v, want a CUSTOM_MINI node and its one replica with append-only persistence", properties)
	}
	for _, typ := range []string{"google_compute_network", "google_compute_subnetwork", "google_network_connectivity_service_connection_policy"} {
		if scope := resourceOfType(t, set, typ).GetScope(); scope != "project:shop/shared:production" {
			t.Errorf("the %s is listed under %s, want the tier it is bootstrapped for", typ, scope)
		}
	}
}

func TestInventoryOfAPreviewStoreIsAMemorystoreNodeWithoutAReplica(t *testing.T) {
	set := inventoryOfAStore(t, &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-42"}, "256mb")

	if replicas := resourceOfType(t, set, "google_memorystore_instance").GetProperties().AsMap()["replica_count"]; replicas != float64(0) {
		t.Errorf("a preview store is listed with %v replicas, want none", replicas)
	}
}

func inventoryOfADatabase(t *testing.T) *costv1.ResourceSet {
	t.Helper()
	return inventoryOfADatabaseIn(t, &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION})
}

func inventoryOfADatabaseIn(t *testing.T, environment *environmentv1.Environment) *costv1.ResourceSet {
	t.Helper()
	client := inventoryServedIn(t, "us-central1")
	manifest := shopManifest()
	manifest.Resources = append(manifest.Resources, &contractv1.ManifestResource{
		LogicalName: "postgres--orders",
		Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "orders"},
		Config:      &contractv1.ManifestResource_Postgres{Postgres: &resourcesv1.PostgresConfig{Version: "17"}},
	})
	set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest:    manifest,
		Environment: environment,
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}
	return set
}

func TestInventoryOfADeclaredDatabaseIsOneCloudSQLInstanceOnTheTiersNetwork(t *testing.T) {
	set := inventoryOfADatabase(t)

	instance := resourceOfType(t, set, "google_sql_database_instance")
	if instance.GetScope() != "project:shop/environment:prod" {
		t.Errorf("the database is listed under %s, want the environment that declares it", instance.GetScope())
	}
	properties := instance.GetProperties().AsMap()
	settings, _ := properties["settings"].(map[string]any)
	if properties["database_version"] != "POSTGRES_17" || settings["tier"] != "db-custom-1-3840" || settings["availability_type"] != "REGIONAL" || settings["disk_size"] != float64(10) {
		t.Errorf("the database is listed as %v, want POSTGRES_17 on a regional db-custom-1-3840 with 10 GB of disk", properties)
	}
	for _, typ := range []string{"google_compute_network", "google_compute_subnetwork", "google_network_connectivity_service_connection_policy"} {
		if scope := resourceOfType(t, set, typ).GetScope(); scope != "project:shop/shared:production" {
			t.Errorf("the %s is listed under %s, want the tier it is bootstrapped for", typ, scope)
		}
	}
	if counts := typeCounts(set); counts["google_network_connectivity_service_connection_policy"] != 2 {
		t.Errorf("counts = %v, want the network's two connection policies, Memorystore's and Cloud SQL's", counts)
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

func TestInventoryOfTopicsAndTasksIsTheirPubSubTopologyTheTiersQueueAndDatabaseAndEachWorkerBesideTheProjectsRecords(t *testing.T) {
	client := inventoryServed(t)

	set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest:    topicsManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}
	counts := typeCounts(set)
	for typ, want := range map[string]int{
		"google_pubsub_topic":         2,
		"google_pubsub_subscription":  3,
		"google_cloud_tasks_queue":    1,
		"google_firestore_database":   2,
		"google_cloud_run_v2_service": 4,
		"google_cloud_scheduler_job":  2,
	} {
		if counts[typ] != want {
			t.Errorf("the inventory lists %d %s, want %d: %v", counts[typ], typ, want, counts)
		}
	}
	for _, r := range set.GetResources() {
		switch {
		case r.GetType() == "google_pubsub_topic" || r.GetType() == "google_pubsub_subscription":
			if r.GetScope() != "project:shop/environment:prod" {
				t.Errorf("%s is listed under %s, want the environment that declares it", r.GetName(), r.GetScope())
			}
		case r.GetType() == "google_cloud_tasks_queue":
			if r.GetScope() != "project:shop/shared:production" {
				t.Errorf("the delay queue is listed under %s, want the tier it is bootstrapped for", r.GetScope())
			}
		case r.GetType() == "google_cloud_run_v2_service" && r.GetScope() == "project:shop/environment:prod/app:web" && r.GetProperties().AsMap()["ingress"] == "INGRESS_TRAFFIC_INTERNAL_ONLY":
			template := r.GetProperties().AsMap()["template"].(map[string]any)
			if template["scaling"].(map[string]any)["min_instance_count"] != float64(0) {
				t.Errorf("the worker %s keeps instances warm, want it scaled from zero", r.GetName())
			}
		}
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

func TestInventoryOfAServerlessNextAppListsNoTaskQueueOrAccountBeyondAContainerAppsOwn(t *testing.T) {
	client := inventoryServed(t)
	inventory := func(container bool) *costv1.ResourceSet {
		set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
			Manifest:    nextManifest(container),
			Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		})
		if err != nil {
			t.Fatalf("Inventory() = %v", err)
		}
		return set
	}

	counts, without := typeCounts(inventory(false)), typeCounts(inventory(true))

	if counts["google_cloud_tasks_queue"] != 0 {
		t.Errorf("counts = %v, want no delay queue: a Next app that declares no topic or task queues nothing", counts)
	}
	if counts["google_service_account"] != without["google_service_account"] {
		t.Errorf("counts = %v, want the %d accounts of a container app", counts, without["google_service_account"])
	}
	if counts["google_firestore_database"] != without["google_firestore_database"] {
		t.Errorf("counts = %v, want no task database", counts)
	}
}

func TestInventoryOfANextAppOnContainerComputeListsNoTaskQueue(t *testing.T) {
	client := inventoryServed(t)

	set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest:    nextManifest(true),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}

	if counts := typeCounts(set); counts["google_cloud_tasks_queue"] != 0 {
		t.Errorf("counts = %v, want no queue for an app that refreshes on its own", counts)
	}
}

func TestANextAppIsListedAtTheMemoryItsServicesRunWith(t *testing.T) {
	client := inventoryServed(t)
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

	set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest:    manifest,
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}

	want := map[string]string{
		"project:shop/environment:prod/app:site": "2048Mi",
		"project:shop/environment:prod/app:docs": "2048Mi",
		"project:shop/environment:prod/app:api":  "512Mi",
	}
	seen := map[string]bool{}
	for _, r := range set.GetResources() {
		memory, named := want[r.GetScope()]
		if r.GetType() != "google_cloud_run_v2_service" || !named {
			continue
		}
		seen[r.GetScope()] = true
		template := r.GetProperties().AsMap()["template"].(map[string]any)
		limits := template["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)["limits"].(map[string]any)
		if limits["memory"] != memory {
			t.Errorf("%s is listed at %v of memory, want the %s its services run with", r.GetScope(), limits["memory"], memory)
		}
	}
	if len(seen) != len(want) {
		t.Errorf("listed the services of %v, want one for each of %v", seen, want)
	}
}

func realtimeManifest() *contractv1.Manifest {
	manifest := shopManifest()
	manifest.Resources = append(manifest.Resources, &contractv1.ManifestResource{
		LogicalName: "realtime--app",
		Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME, Name: "app"},
		Config: &contractv1.ManifestResource_Realtime{Realtime: &resourcesv1.RealtimeConfig{
			Channels: []*resourcesv1.RealtimeChannel{{
				Pattern:   "orders/:orderId",
				Subscribe: resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_RULE,
				Publish:   resourcesv1.RealtimePublish_REALTIME_PUBLISH_SERVER,
			}},
		}},
	})
	return manifest
}

func TestARealtimeIsListedAsItsEnvironmentsGatewayTopicKeysAndSigningKey(t *testing.T) {
	client := inventoryServed(t)

	set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest:    realtimeManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}
	names := namesOf(t, newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"}))
	want := map[string]bool{
		"google_cloud_run_v2_service:" + gatewayOf(t, names, "shop", "prod"):                                         true,
		"google_pubsub_topic:" + gatewayOf(t, names, "shop", "prod"):                                                 true,
		"google_pubsub_subscription:" + gatewayOf(t, names, "shop", "prod"):                                          true,
		"google_secret_manager_secret:" + must(names.RealtimeKeysSecret(environment.TierProduction, "shop", "prod")): true,
		"google_secret_manager_secret:" + must(names.RealtimeSigningSecret("shop", "prod", "app")):                   true,
	}
	for _, resource := range set.GetResources() {
		key := resource.GetType() + ":" + resource.GetName()
		if want[key] {
			if resource.GetScope() != "project:shop/environment:prod" {
				t.Errorf("%s is listed under %s, want it under the environment", key, resource.GetScope())
			}
			delete(want, key)
		}
	}
	if len(want) > 0 {
		t.Errorf("the inventory leaves out %v", want)
	}
}
