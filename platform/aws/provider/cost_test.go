package aws_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
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
	"github.com/ocelhq/ocel/pkg/provider/resources"
	aws "github.com/ocelhq/ocel/platform/aws/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func costServed(t *testing.T) (contractv1connect.ProviderServiceClient, costv1connect.CostServiceClient) {
	t.Helper()
	p := aws.NewProvider(control.Options{Region: "us-east-1"}, nil, awssdk.Config{Region: "us-east-1"}, defaultNamespace)
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
			{Name: "web", Framework: &contractv1.Framework{Name: "next", Arch: "arm64"},
				Artifact: &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{Functions: []*contractv1.ManifestFunction{
					{LogicalName: "fn--web--entry", Framework: &contractv1.Framework{Name: "next", Arch: "arm64"}},
				}}}},
			{Name: "api", Framework: &contractv1.Framework{Name: "go"},
				Artifact: &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{
					Image: "ghcr.io/shop/api@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", HealthCheckPath: "/healthz",
					MinInstances: 1, MaxInstances: 1,
				}}},
		},
		Resources: []*contractv1.ManifestResource{
			{LogicalName: "main", Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "main"}, Config: &contractv1.ManifestResource_Postgres{Postgres: &resourcesv1.PostgresConfig{}}},
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

func TestInventoryDescribesAProductionDeployBehindCloudFront(t *testing.T) {
	client, _ := costServed(t)

	set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest:    shopManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}
	golden(t, "inventory_production_cloudfront", set)

	counts := typeCounts(set)
	if counts["aws_cloudfront_distribution"] != 1 || counts["aws_lb"] != 1 || counts["aws_rds_cluster"] != 1 || counts["aws_kms_key"] != 1 || counts["aws_data_transfer"] != 0 {
		t.Errorf("counts = %v, want the distribution, the container infrastructure's load balancer, the cluster, the variables key and no origin egress behind CloudFront", counts)
	}
	if counts["aws_lambda_function"] != 1+1+2+1 {
		t.Errorf("lambdas = %d, want the app's, the upload completer's, the two the next runtime's parts provision and the variables key's env source sync", counts["aws_lambda_function"])
	}
	for _, r := range set.GetResources() {
		if r.GetVendor() != "aws" || r.GetRegion() != "us-east-1" {
			t.Errorf("%s is %s in %q", r.GetId(), r.GetVendor(), r.GetRegion())
		}
	}
}

func TestInventoryOfAPreviewHasItsOwnTierAndWildcard(t *testing.T) {
	client, _ := costServed(t)

	set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest:    shopManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-42"},
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}
	golden(t, "inventory_preview_cloudfront", set)

	if typeCounts(set)["aws_cloudfront_distribution"] != 2 {
		t.Errorf("a preview tier fronts every preview through one wildcard distribution beside the project's own")
	}
	kinds := map[string]string{}
	for _, scope := range set.GetScopes() {
		kinds[scope.GetKind()] = scope.GetName()
	}
	if kinds["shared"] != "preview" || kinds["environment"] != "pr-42" {
		t.Errorf("scopes = %v", kinds)
	}
}

func TestInventoryOfAnEphemeralPreviewPricesTheDatabaseAndBucketItProvisions(t *testing.T) {
	client, _ := costServed(t)

	set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest: shopManifest(),
		Environment: &environmentv1.Environment{
			Tier:      environmentv1.Tier_TIER_PREVIEW,
			Lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
			Identity:  "pr-42",
		},
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}

	var priced []string
	for _, resource := range set.GetResources() {
		priced = append(priced, resource.GetName())
	}
	for _, want := range []string{"main", "uploads"} {
		if !slices.Contains(priced, want) {
			t.Errorf("an ephemeral preview priced %v, want %s: its infra stack provisions it", priced, want)
		}
	}
	if counts := typeCounts(set); counts["aws_rds_cluster"] == 0 {
		t.Error("an ephemeral preview priced no aurora cluster, want the one it provisions")
	}
}

func TestInventoryOfAnEphemeralPreviewPricesTheContainerWorkersAndSchedulesItDeclares(t *testing.T) {
	client, _ := costServed(t)

	manifest := shopManifest()
	manifest.Workers = []*contractv1.ManifestWorker{{Name: "media", App: "api", Compute: string(provider.ComputeContainer)}}
	manifest.Resources = append(manifest.Resources, &contractv1.ManifestResource{
		LogicalName: "task--nightly-report",
		Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, Name: "nightly-report"},
		Config: &contractv1.ManifestResource_Topic{Topic: &contractv1.ManifestTopic{
			Cron:      "0 3 * * *",
			Consumers: []*contractv1.ManifestConsumer{{Name: "nightly-report", Worker: "media", Exclusive: true}},
		}},
	})
	set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest: manifest,
		Environment: &environmentv1.Environment{
			Tier:      environmentv1.Tier_TIER_PREVIEW,
			Lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
			Identity:  "pr-42",
		},
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}

	priced := map[string]bool{}
	for _, resource := range set.GetResources() {
		priced[resource.GetType()+":"+resource.GetName()] = true
	}
	for _, want := range []string{"aws_ecs_service:" + resources.WorkerName("media"), "aws_sqs_queue:nightly-report/nightly-report", "aws_scheduler_schedule:nightly-report"} {
		if !priced[want] {
			t.Errorf("an ephemeral preview priced %v, want %s: it runs its container workers and their schedules as a persistent environment does", slices.Sorted(maps.Keys(priced)), want)
		}
	}
}

func TestInventoryBehindAPIGatewayProvisionsARestAPIPerDeploy(t *testing.T) {
	client, _ := costServed(t)

	manifest := shopManifest()
	manifest.Apps = manifest.Apps[:1]
	set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest:    manifest,
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		Edge:        &contractv1.EdgeSelection{Kind: string(apigateway.Kind)},
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}
	golden(t, "inventory_production_apigateway", set)

	counts := typeCounts(set)
	if counts["aws_api_gateway_rest_api"] != 1 || counts["aws_cloudfront_distribution"] != 0 || counts["aws_lb"] != 0 || counts["aws_data_transfer"] != 1 {
		t.Errorf("counts = %v, want the project's own REST API, the app's egress, no CloudFront and no container load balancer: the 404 responder is a part only the preview wildcard adds", counts)
	}
}

func TestInventoryBehindCloudflarePricesTheInternetFacingLoadBalancerItsContainerAppIsForwardedTo(t *testing.T) {
	client, _ := costServed(t)

	set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest:    shopManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		Edge:        &contractv1.EdgeSelection{Kind: string(cloudflare.Kind)},
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}

	var internal []bool
	for _, r := range set.GetResources() {
		if r.GetType() == "aws_lb" {
			internal = append(internal, r.GetProperties().AsMap()["internal"].(bool))
		}
	}
	if !slices.Equal(internal, []bool{false}) {
		t.Errorf("load balancers internal = %v, want the one internet-facing load balancer Cloudflare forwards the container app to", internal)
	}
}

func TestInventoryWithABroughtVariablesKeyProvisionsNoKey(t *testing.T) {
	p := aws.NewProvider(control.Options{Region: "us-east-1", VariablesKey: "arn:aws:kms:us-east-1:1:key/k"}, nil, awssdk.Config{Region: "us-east-1"}, defaultNamespace)
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

	set, err := client.Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest:    shopManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}
	if typeCounts(set)["aws_kms_key"] != 0 {
		t.Errorf("counts = %v, want no KMS key when the account brought one", typeCounts(set))
	}
}
