package providerserver_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/cost/v1/costv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

type uncosted struct{ provider.Provider }

func (u uncosted) Hooks() provider.Hooks {
	hooks := u.Provider.Hooks()
	hooks.Cost = nil
	return hooks
}

func costServed(t *testing.T, p provider.Provider) (contractv1connect.ProviderServiceClient, costv1connect.CostServiceClient) {
	t.Helper()
	config := providerserver.Config{
		Version: "1.0.0",
		New:     func(context.Context, provider.Settings) (provider.Provider, error) { return p, nil },
	}
	server := httptest.NewServer(providerserver.ConformanceMux(config))
	t.Cleanup(server.Close)
	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := client.Configure(context.Background(), configureInWorkingDir(t)); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	return client, costv1connect.NewCostServiceClient(server.Client(), server.URL)
}

func shapeRequest() *contractv1.ShapeRequest {
	req := twoAppRequest()
	return &contractv1.ShapeRequest{
		Manifest:    req.GetManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		Edge:        req.GetEdge(),
	}
}

func TestAProviderThatPricesNothingSaysSoOnShapeAndPrice(t *testing.T) {
	client, costs := costServed(t, uncosted{fake.NewProvider(fake.Options{Region: "nowhere"})})

	_, err := client.Shape(context.Background(), shapeRequest())
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("Shape() error = %v, want Unimplemented", err)
	}
	_, err = costs.Price(context.Background(), &costv1.PriceRequest{Resources: &costv1.ResourceSet{Source: "ocel"}})
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("Price() error = %v, want Unimplemented", err)
	}
}

func TestShapeHandsTheProviderTheDeployItWouldMake(t *testing.T) {
	client, _ := costServed(t, fake.NewProvider(fake.Options{Region: "nowhere"}))

	set, err := client.Shape(context.Background(), shapeRequest())
	if err != nil {
		t.Fatalf("Shape() error = %v", err)
	}
	if set.GetSource() != "ocel" {
		t.Errorf("source = %q, want ocel", set.GetSource())
	}
	kinds := map[string]int{}
	for _, scope := range set.GetScopes() {
		kinds[scope.GetKind()]++
	}
	if kinds["project"] != 1 || kinds["environment"] != 1 || kinds["app"] != 2 {
		t.Errorf("scope kinds = %v, want one project, one environment, two apps", kinds)
	}
	for _, resource := range set.GetResources() {
		if resource.GetVendor() != "fake" || resource.GetRegion() != "nowhere" {
			t.Errorf("resource %s has vendor %q region %q", resource.GetId(), resource.GetVendor(), resource.GetRegion())
		}
	}
}

func countShapedTypes(set *costv1.ResourceSet) map[string]int {
	types := map[string]int{}
	for _, resource := range set.GetResources() {
		types[resource.GetType()]++
	}
	return types
}

func TestShapeOfAnEphemeralPreviewPricesNoResourceItNeverProvisions(t *testing.T) {
	client, _ := costServed(t, fake.NewProvider(fake.Options{Region: "nowhere"}))
	req := shapeRequest()
	req.Environment = &environmentv1.Environment{
		Tier:      environmentv1.Tier_TIER_PREVIEW,
		Lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
		Identity:  "preview",
	}

	set, err := client.Shape(context.Background(), req)
	if err != nil {
		t.Fatalf("Shape() error = %v", err)
	}
	types := countShapedTypes(set)
	if types[fake.TypePostgres] != 0 || types[fake.TypeBucket] != 0 {
		t.Errorf("shaped types = %v, want no postgres or bucket: an ephemeral preview has no infra stack of its own", types)
	}
	if types[fake.TypeFunction] != 2 {
		t.Errorf("shaped types = %v, want both apps' functions still priced", types)
	}
}

func TestShapeOfAPersistentPreviewPricesTheResourcesItProvisions(t *testing.T) {
	client, _ := costServed(t, fake.NewProvider(fake.Options{Region: "nowhere"}))
	req := shapeRequest()
	req.Environment = &environmentv1.Environment{
		Tier:      environmentv1.Tier_TIER_PREVIEW,
		Lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT,
		Identity:  "staging",
	}

	set, err := client.Shape(context.Background(), req)
	if err != nil {
		t.Fatalf("Shape() error = %v", err)
	}
	if types := countShapedTypes(set); types[fake.TypePostgres] != 1 || types[fake.TypeBucket] != 1 {
		t.Errorf("shaped types = %v, want the postgres and the bucket its infra stack provisions", types)
	}
}

func TestPriceRefusesAUsageFileTheProviderCannotRead(t *testing.T) {
	client, costs := costServed(t, fake.NewProvider(fake.Options{Region: "nowhere"}))

	set, err := client.Shape(context.Background(), shapeRequest())
	if err != nil {
		t.Fatalf("Shape() error = %v", err)
	}
	override, _ := structpb.NewStruct(map[string]any{"monthly_requets": 5})
	_, err = costs.Price(context.Background(), &costv1.PriceRequest{
		Resources: set,
		Usage:     &costv1.Usage{Resources: map[string]*structpb.Struct{set.GetResources()[0].GetId(): override}},
	})
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "monthly_requets") {
		t.Fatalf("Price() error = %v, want InvalidArgument naming the key nothing reads", err)
	}
}

func TestConfigureSaysWhetherTheProviderPricesADeploy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		served provider.Provider
		want   bool
	}{
		{"a provider with cost hooks", fake.NewProvider(fake.Options{Region: "nowhere"}), true},
		{"a provider without them", uncosted{fake.NewProvider(fake.Options{Region: "nowhere"})}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := providerserver.Config{
				Version: "1.0.0",
				New:     func(context.Context, provider.Settings) (provider.Provider, error) { return tc.served, nil },
			}
			server := httptest.NewServer(providerserver.ConformanceMux(config))
			t.Cleanup(server.Close)

			configured, err := contractv1connect.NewProviderServiceClient(server.Client(), server.URL).
				Configure(context.Background(), configureInWorkingDir(t))
			if err != nil {
				t.Fatalf("Configure() error = %v", err)
			}
			if got := configured.GetFacts().GetPricesDeploys(); got != tc.want {
				t.Errorf("Configure() says prices_deploys = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestPriceRefusesAnEmptyResourceSet(t *testing.T) {
	_, costs := costServed(t, fake.NewProvider(fake.Options{Region: "nowhere"}))

	_, err := costs.Price(context.Background(), &costv1.PriceRequest{})
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("Price() error = %v, want InvalidArgument", err)
	}
}
