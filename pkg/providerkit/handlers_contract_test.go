package providerkit_test

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

func TestConfigureHandsTheProviderTheTransformModulesTheProjectLists(t *testing.T) {
	var seen providerkit.Settings
	spec := providerkit.Spec{
		Version: "test",
		New: func(_ context.Context, settings providerkit.Settings) (providerkit.Provider, error) {
			seen = settings
			return fake.Full{Provider: fake.NewProvider(fake.Options{})}, nil
		},
	}
	server := httptest.NewServer(providerkit.ConformanceMux(spec))
	t.Cleanup(server.Close)

	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	modules := []string{"./transforms/network.transform.ts", "./transforms/tags.transform.ts"}
	if _, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{
		Config: &contractv1.ProviderConfig{Transforms: modules},
	}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}

	if !slices.Equal(seen.Transforms, modules) {
		t.Errorf("Transforms = %v, want %v", seen.Transforms, modules)
	}
}
