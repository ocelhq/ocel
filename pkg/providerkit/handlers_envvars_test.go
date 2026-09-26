package providerkit_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1/envvarsv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

const slug = "shop"

func varsServedBy(t *testing.T, p provider.Provider) envvarsv1connect.EnvVarsServiceClient {
	t.Helper()

	spec := providerkit.Spec{
		Version: "test",
		New: func(context.Context, provider.Settings) (provider.Provider, error) {
			return p, nil
		},
	}
	server := httptest.NewServer(providerkit.ConformanceMux(spec))
	t.Cleanup(server.Close)

	contract := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := contract.Configure(context.Background(), &contractv1.ConfigureRequest{}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	return envvarsv1connect.NewEnvVarsServiceClient(server.Client(), server.URL)
}

func cell(key string) *envvarsv1.Coordinate {
	return &envvarsv1.Coordinate{Slug: slug, Key: key}
}

type refusingGrants struct{ *fake.Provider }

func (r refusingGrants) Hooks() provider.Hooks {
	hooks := r.Provider.Hooks()
	hooks.VerifyGrants = r.VerifyGrants
	return hooks
}

func (refusingGrants) VerifyGrants(context.Context, provider.Binding) error {
	return fmt.Errorf("s3:* names a whole service: %w", provider.ErrUnscopedGrant)
}

func TestSetBindingAsksTheProviderWhetherItWouldGrantThat(t *testing.T) {
	vars := varsServedBy(t, refusingGrants{fake.NewProvider(fake.Options{})})

	_, err := vars.SetBinding(context.Background(), &envvarsv1.SetBindingRequest{
		Slug:  slug,
		Tier:  environmentv1.Tier_TIER_PRODUCTION,
		Owner: "acme",
		Binding: &bindingsv1.Binding{
			Name:       "files",
			Source:     "acme",
			Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: "files"}},
			Grants:     []*bindingsv1.Grant{{Actions: []string{"s3:*"}, Resources: []string{"arn:aws:s3:::files"}}},
		},
	})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("SetBinding(): code = %v, want %v: the provider refused the grant", got, connect.CodeInvalidArgument)
	}
}

func TestEveryValueRPCRefusesBeforeConfigure(t *testing.T) {
	spec := providerkit.Spec{
		Version: "test",
		New: func(context.Context, provider.Settings) (provider.Provider, error) {
			return fake.NewProvider(fake.Options{}), nil
		},
	}
	server := httptest.NewServer(providerkit.ConformanceMux(spec))
	t.Cleanup(server.Close)

	vars := envvarsv1connect.NewEnvVarsServiceClient(server.Client(), server.URL)

	ctx := context.Background()
	calls := map[string]func() error{
		"SetValue": func() error {
			_, err := vars.SetValue(ctx, &envvarsv1.SetValueRequest{Coordinate: cell("KEY")})
			return err
		},
		"ListValues": func() error {
			_, err := vars.ListValues(ctx, &envvarsv1.ListValuesRequest{Slug: slug})
			return err
		},
		"GetValue": func() error {
			_, err := vars.GetValue(ctx, &envvarsv1.GetValueRequest{Coordinate: cell("KEY")})
			return err
		},
		"RevealValues": func() error {
			_, err := vars.RevealValues(ctx, &envvarsv1.RevealValuesRequest{Slug: slug})
			return err
		},
		"DeleteValue": func() error {
			_, err := vars.DeleteValue(ctx, &envvarsv1.DeleteValueRequest{Coordinate: cell("KEY")})
			return err
		},
		"SetReference": func() error {
			_, err := vars.SetReference(ctx, &envvarsv1.SetReferenceRequest{Coordinate: cell("KEY"), Target: cell("OTHER")})
			return err
		},
		"ListReferences": func() error {
			_, err := vars.ListReferences(ctx, &envvarsv1.ListReferencesRequest{Coordinate: cell("KEY")})
			return err
		},
		"ListVersions": func() error {
			_, err := vars.ListVersions(ctx, &envvarsv1.ListVersionsRequest{Coordinate: cell("KEY")})
			return err
		},
		"SetBinding": func() error {
			_, err := vars.SetBinding(ctx, &envvarsv1.SetBindingRequest{
				Slug:  slug,
				Tier:  environmentv1.Tier_TIER_PRODUCTION,
				Owner: "acme",
				Binding: &bindingsv1.Binding{
					Name:       "db",
					Source:     "acme",
					Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: "files"}},
				},
			})
			return err
		},
		"RemoveBinding": func() error {
			_, err := vars.RemoveBinding(ctx, &envvarsv1.RemoveBindingRequest{Slug: slug, Tier: environmentv1.Tier_TIER_PRODUCTION, Name: "db"})
			return err
		},
		"ListBindings": func() error {
			_, err := vars.ListBindings(ctx, &envvarsv1.ListBindingsRequest{Slug: slug, Tier: environmentv1.Tier_TIER_PRODUCTION})
			return err
		},
	}
	if len(calls) != 11 {
		t.Fatalf("the suite drives %d of EnvVarsService's 11 RPCs", len(calls))
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if got := connect.CodeOf(call()); got != connect.CodeFailedPrecondition {
				t.Fatalf("%s before Configure: code = %v, want %v", name, got, connect.CodeFailedPrecondition)
			}
		})
	}
}
