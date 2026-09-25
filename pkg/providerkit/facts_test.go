package providerkit_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

type renderingTransforms struct{ *fake.Provider }

func (r renderingTransforms) Facts() providerkit.Facts {
	facts := r.Provider.Facts()
	facts.RendersTransforms = true
	return facts
}

func servedListingTransforms(t *testing.T, provider providerkit.Provider) contractv1connect.ProviderServiceClient {
	t.Helper()
	spec := providerkit.Spec{
		Version: "1.0.0",
		New:     func(context.Context, providerkit.Settings) (providerkit.Provider, error) { return provider, nil },
	}
	server := httptest.NewServer(providerkit.ConformanceMux(spec))
	t.Cleanup(server.Close)

	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{
		Config: &contractv1.ProviderConfig{Transforms: []string{"./transforms/tags.transform.ts"}},
	}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	standsBootstrapped(t, client)
	return client
}

func TestADeployListingTransformsIsRefusedByAProviderWhoseFactsSayItRendersNone(t *testing.T) {
	builtProject(t)
	client := servedListingTransforms(t, fake.NewProvider(fake.Options{}))

	result, _, err := deployStream(t, client, deployRequest())
	if err == nil || result.GetSuccess() {
		t.Fatal("Deploy() listing a transform succeeded through a provider whose facts say it renders none, want it refused before anything is stood up")
	}
	if !strings.Contains(err.Error(), "tags.transform.ts") {
		t.Errorf("Deploy() error = %q, want it to name the transform nothing here would run", err)
	}
}

func TestADeployListingTransformsGoesThroughAProviderWhoseFactsSayItRendersThem(t *testing.T) {
	builtProject(t)
	client := servedListingTransforms(t, renderingTransforms{Provider: fake.NewProvider(fake.Options{})})

	result, _ := deploy(t, client, deployRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want a provider whose facts say it renders transforms handed the deploy", result.GetError())
	}
}
