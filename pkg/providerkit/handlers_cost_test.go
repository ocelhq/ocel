package providerkit_test

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
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

type uncosted struct{ providerkit.Provider }

func costServed(t *testing.T, provider providerkit.Provider) (contractv1connect.ProviderServiceClient, costv1connect.CostServiceClient) {
	t.Helper()
	spec := providerkit.Spec{
		Version: "1.0.0",
		New:     func(context.Context, providerkit.Settings) (providerkit.Provider, error) { return provider, nil },
	}
	server := httptest.NewServer(providerkit.ConformanceMux(spec))
	t.Cleanup(server.Close)
	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	return client, costv1connect.NewCostServiceClient(server.Client(), server.URL)
}

func inventoryRequest() *contractv1.InventoryRequest {
	req := twoAppRequest()
	return &contractv1.InventoryRequest{
		Manifest:    req.GetManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		Edge:        req.GetEdge(),
	}
}

func TestAProviderThatPricesNothingSaysSoOnInventoryAndPrice(t *testing.T) {
	client, pricer := costServed(t, uncosted{fake.NewProvider(fake.Options{Region: "nowhere"})})

	_, err := client.Inventory(context.Background(), inventoryRequest())
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("Inventory() error = %v, want Unimplemented", err)
	}
	_, err = pricer.Price(context.Background(), &costv1.PriceRequest{Resources: &costv1.ResourceSet{Source: "ocel"}})
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("Price() error = %v, want Unimplemented", err)
	}
}

func TestInventoryHandsTheProviderTheDeployItWouldMake(t *testing.T) {
	client, _ := costServed(t, fake.NewProvider(fake.Options{Region: "nowhere"}))

	set, err := client.Inventory(context.Background(), inventoryRequest())
	if err != nil {
		t.Fatalf("Inventory() error = %v", err)
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
			t.Errorf("resource %s carries vendor %q region %q", resource.GetId(), resource.GetVendor(), resource.GetRegion())
		}
	}
}

func TestPriceRefusesAUsageFileTheProviderCannotRead(t *testing.T) {
	client, pricer := costServed(t, fake.NewProvider(fake.Options{Region: "nowhere"}))

	set, err := client.Inventory(context.Background(), inventoryRequest())
	if err != nil {
		t.Fatalf("Inventory() error = %v", err)
	}
	override, _ := structpb.NewStruct(map[string]any{"monthly_requets": 5})
	_, err = pricer.Price(context.Background(), &costv1.PriceRequest{
		Resources: set,
		Usage:     &costv1.Usage{Resources: map[string]*structpb.Struct{set.GetResources()[0].GetId(): override}},
	})
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "monthly_requets") {
		t.Fatalf("Price() error = %v, want InvalidArgument naming the key nothing reads", err)
	}
}

func TestPriceRefusesAnEmptyResourceSet(t *testing.T) {
	_, pricer := costServed(t, fake.NewProvider(fake.Options{Region: "nowhere"}))

	_, err := pricer.Price(context.Background(), &costv1.PriceRequest{})
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("Price() error = %v, want InvalidArgument", err)
	}
}
