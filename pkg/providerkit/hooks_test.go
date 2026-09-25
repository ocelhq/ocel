package providerkit_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/google/go-containerregistry/pkg/v1"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

func configuring(t *testing.T, set func(*providerkit.Hooks)) error {
	t.Helper()
	provider := fake.NewProvider(fake.Options{}).Hook(set)
	spec := providerkit.Spec{
		Version: "1.0.0",
		New:     func(context.Context, providerkit.Settings) (providerkit.Provider, error) { return provider, nil },
	}
	server := httptest.NewServer(providerkit.ConformanceMux(spec))
	t.Cleanup(server.Close)
	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	_, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{})
	return err
}

func shapeCost(context.Context, providerkit.ShapeRequest) (*costv1.ResourceSet, error) {
	return &costv1.ResourceSet{}, nil
}

func estimateCost(context.Context, *costv1.PriceRequest) (*costv1.Estimate, error) {
	return &costv1.Estimate{}, nil
}

func functionBaseImage(context.Context, providerkit.Framework) (v1.Image, error) { return nil, nil }

func functionRuntime(context.Context, providerkit.Framework) ([]byte, error) { return nil, nil }

func TestConfigureRefusesAProviderWhoseHooksSetHalfOfAPair(t *testing.T) {
	for _, tc := range []struct {
		name  string
		set   func(*providerkit.Hooks)
		names []string
	}{
		{"a cost shape nothing prices", func(h *providerkit.Hooks) {
			h.ShapeCost, h.EstimateCost = shapeCost, nil
		}, []string{"ShapeCost", "EstimateCost"}},
		{"a price for nothing shaped", func(h *providerkit.Hooks) {
			h.ShapeCost, h.EstimateCost = nil, estimateCost
		}, []string{"ShapeCost", "EstimateCost"}},
		{"a function base image with no runtime to boot it", func(h *providerkit.Hooks) {
			h.FunctionBaseImage = functionBaseImage
		}, []string{"FunctionBaseImage", "FunctionRuntime"}},
		{"a function runtime with no base image to put it on", func(h *providerkit.Hooks) {
			h.FunctionRuntime = functionRuntime
		}, []string{"FunctionBaseImage", "FunctionRuntime"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := configuring(t, tc.set)
			if err == nil {
				t.Fatal("Configure() accepted a provider whose hooks set one half of a pair, want the session refused before any call reaches the half that is missing")
			}
			if connect.CodeOf(err) != connect.CodeInternal {
				t.Errorf("Configure() code = %v, want %v: the provider was built wrong, and nothing the caller sends fixes it", connect.CodeOf(err), connect.CodeInternal)
			}
			for _, want := range tc.names {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Configure() error = %q, want it to name %s", err, want)
				}
			}
		})
	}
}

func TestConfigureAcceptsAProviderWhoseHooksSetBothHalvesOfEveryPair(t *testing.T) {
	err := configuring(t, func(h *providerkit.Hooks) {
		h.ShapeCost, h.EstimateCost = shapeCost, estimateCost
		h.FunctionBaseImage, h.FunctionRuntime = functionBaseImage, functionRuntime
	})
	if err != nil {
		t.Fatalf("Configure() = %v, want a provider whose pairs are whole configured", err)
	}
}

func TestConfigureAcceptsAProviderThatSetsNeitherHalfOfAPair(t *testing.T) {
	err := configuring(t, func(h *providerkit.Hooks) {
		h.ShapeCost, h.EstimateCost = nil, nil
	})
	if err != nil {
		t.Fatalf("Configure() = %v, want a provider that prices nothing configured: an absent pair is absent", err)
	}
}
