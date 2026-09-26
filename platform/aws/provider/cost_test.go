package aws_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/providerserver"
	aws "github.com/ocelhq/ocel/platform/aws/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func costServed(t *testing.T) (contractv1connect.ProviderServiceClient, costv1connect.CostServiceClient) {
	t.Helper()
	p := aws.NewProvider(aws.Options{Region: "us-east-1"}, nil, awssdk.Config{Region: "us-east-1"}, defaultNamespace)
	config := providerserver.Config{
		Version: "test",
		New:     func(context.Context, provider.Settings) (provider.Provider, error) { return p, nil },
	}
	server := httptest.NewServer(providerserver.ConformanceMux(config))
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
			{Name: "web", Framework: &contractv1.Framework{Name: "next", Arch: "arm64"}, Compute: "serverless"},
			{Name: "api", Framework: &contractv1.Framework{Name: "go"}, Compute: "container"},
		},
		Functions: []*contractv1.ManifestFunction{
			{LogicalName: "fn--web--entry", App: "web", Framework: &contractv1.Framework{Name: "next", Arch: "arm64"}},
		},
		Containers: []*contractv1.ManifestContainer{
			{App: "api", Image: "ghcr.io/shop/api@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", HealthCheckPath: "/healthz"},
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

func TestShapeDescribesAProductionDeployBehindCloudFront(t *testing.T) {
	client, _ := costServed(t)

	set, err := client.Shape(context.Background(), &contractv1.ShapeRequest{
		Manifest:    shopManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Shape() = %v", err)
	}
	golden(t, "shape_production_cloudfront", set)

	counts := typeCounts(set)
	if counts["aws_cloudfront_distribution"] != 1 || counts["aws_lb"] != 1 || counts["aws_rds_cluster"] != 1 || counts["aws_kms_key"] != 1 || counts["aws_data_transfer"] != 0 {
		t.Errorf("counts = %v, want the distribution, the container infrastructure's load balancer, the cluster, the vars key and no origin egress behind CloudFront", counts)
	}
	if counts["aws_lambda_function"] != 1+1+3 {
		t.Errorf("lambdas = %d, want the app's, the upload completer's and the three the next runtime's features provision", counts["aws_lambda_function"])
	}
	for _, r := range set.GetResources() {
		if r.GetVendor() != "aws" || r.GetRegion() != "us-east-1" {
			t.Errorf("%s is %s in %q", r.GetId(), r.GetVendor(), r.GetRegion())
		}
	}
}

func TestShapeOfAPreviewHasItsOwnClassAndWildcard(t *testing.T) {
	client, _ := costServed(t)

	set, err := client.Shape(context.Background(), &contractv1.ShapeRequest{
		Manifest:    shopManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-42"},
	})
	if err != nil {
		t.Fatalf("Shape() = %v", err)
	}
	golden(t, "shape_preview_cloudfront", set)

	if typeCounts(set)["aws_cloudfront_distribution"] != 2 {
		t.Errorf("a preview class fronts every preview through one wildcard distribution beside the project's own")
	}
	kinds := map[string]string{}
	for _, scope := range set.GetScopes() {
		kinds[scope.GetKind()] = scope.GetName()
	}
	if kinds["shared"] != "preview" || kinds["environment"] != "pr-42" {
		t.Errorf("scopes = %v", kinds)
	}
}

func TestShapeBehindAPIGatewayProvisionsARestAPIPerDeploy(t *testing.T) {
	client, _ := costServed(t)

	manifest := shopManifest()
	manifest.Apps = manifest.Apps[:1]
	manifest.Containers = nil
	set, err := client.Shape(context.Background(), &contractv1.ShapeRequest{
		Manifest:    manifest,
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		Edge:        &contractv1.EdgeSelection{Kind: string(apigateway.Kind)},
	})
	if err != nil {
		t.Fatalf("Shape() = %v", err)
	}
	golden(t, "shape_production_apigateway", set)

	counts := typeCounts(set)
	if counts["aws_api_gateway_rest_api"] != 2 || counts["aws_cloudfront_distribution"] != 0 || counts["aws_lb"] != 0 || counts["aws_data_transfer"] != 1 {
		t.Errorf("counts = %v, want the shared 404 responder and the project's own REST API, the app's egress, no CloudFront and no container load balancer", counts)
	}
}

func TestShapeWithABroughtVarsKeyProvisionsNoKey(t *testing.T) {
	p := aws.NewProvider(aws.Options{Region: "us-east-1", VarsKey: "arn:aws:kms:us-east-1:1:key/k"}, nil, awssdk.Config{Region: "us-east-1"}, defaultNamespace)
	config := providerserver.Config{
		Version: "test",
		New:     func(context.Context, provider.Settings) (provider.Provider, error) { return p, nil },
	}
	server := httptest.NewServer(providerserver.ConformanceMux(config))
	t.Cleanup(server.Close)
	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}

	set, err := client.Shape(context.Background(), &contractv1.ShapeRequest{
		Manifest:    shopManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Shape() = %v", err)
	}
	if typeCounts(set)["aws_kms_key"] != 0 {
		t.Errorf("counts = %v, want no KMS key when the account brought one", typeCounts(set))
	}
}
