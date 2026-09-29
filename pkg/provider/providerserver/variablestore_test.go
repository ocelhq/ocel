package providerserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

const slug = "shop"

func variablesServedBy(t *testing.T, p provider.Provider) variablestorev1connect.VariableStoreServiceClient {
	t.Helper()

	config := providerserver.Config{
		Version: "test",
		New: func(context.Context, provider.Settings) (provider.Provider, error) {
			return p, nil
		},
	}
	server := httptest.NewServer(providerserver.ConformanceMux(config))
	t.Cleanup(server.Close)

	contract := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := contract.Configure(context.Background(), configureInWorkingDir(t)); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	return variablestorev1connect.NewVariableStoreServiceClient(server.Client(), server.URL)
}

func cell(key string) *variablestorev1.Coordinate {
	return &variablestorev1.Coordinate{Slug: slug, Key: key}
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
	variables := variablesServedBy(t, refusingGrants{fake.NewProvider(fake.Options{})})

	_, err := variables.SetBinding(context.Background(), &variablestorev1.SetBindingRequest{
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
	config := providerserver.Config{
		Version: "test",
		New: func(context.Context, provider.Settings) (provider.Provider, error) {
			return fake.NewProvider(fake.Options{}), nil
		},
	}
	server := httptest.NewServer(providerserver.ConformanceMux(config))
	t.Cleanup(server.Close)

	variables := variablestorev1connect.NewVariableStoreServiceClient(server.Client(), server.URL)

	ctx := context.Background()
	calls := map[string]func() error{
		"SetValue": func() error {
			_, err := variables.SetValue(ctx, &variablestorev1.SetValueRequest{Coordinate: cell("KEY")})
			return err
		},
		"ListValues": func() error {
			_, err := variables.ListValues(ctx, &variablestorev1.ListValuesRequest{Slug: slug})
			return err
		},
		"GetValue": func() error {
			_, err := variables.GetValue(ctx, &variablestorev1.GetValueRequest{Coordinate: cell("KEY")})
			return err
		},
		"RevealValues": func() error {
			_, err := variables.RevealValues(ctx, &variablestorev1.RevealValuesRequest{Slug: slug})
			return err
		},
		"DeleteValue": func() error {
			_, err := variables.DeleteValue(ctx, &variablestorev1.DeleteValueRequest{Coordinate: cell("KEY")})
			return err
		},
		"SetReference": func() error {
			_, err := variables.SetReference(ctx, &variablestorev1.SetReferenceRequest{Coordinate: cell("KEY"), Target: cell("OTHER")})
			return err
		},
		"ListReferences": func() error {
			_, err := variables.ListReferences(ctx, &variablestorev1.ListReferencesRequest{Coordinate: cell("KEY")})
			return err
		},
		"ListVersions": func() error {
			_, err := variables.ListVersions(ctx, &variablestorev1.ListVersionsRequest{Coordinate: cell("KEY")})
			return err
		},
		"SetBinding": func() error {
			_, err := variables.SetBinding(ctx, &variablestorev1.SetBindingRequest{
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
			_, err := variables.RemoveBinding(ctx, &variablestorev1.RemoveBindingRequest{Slug: slug, Tier: environmentv1.Tier_TIER_PRODUCTION, Name: "db"})
			return err
		},
		"ListBindings": func() error {
			_, err := variables.ListBindings(ctx, &variablestorev1.ListBindingsRequest{Slug: slug, Tier: environmentv1.Tier_TIER_PRODUCTION})
			return err
		},
		"SyncEnvSource": func() error {
			_, err := variables.SyncEnvSource(ctx, &variablestorev1.SyncEnvSourceRequest{
				Slug: slug,
				Tier: environmentv1.Tier_TIER_PRODUCTION,
				From: &variablestorev1.SyncEnvSourceRequest_Registered{Registered: &variablestorev1.RegisteredEnvSource{}},
			})
			return err
		},
		"DescribeEnvSource": func() error {
			_, err := variables.DescribeEnvSource(ctx, &variablestorev1.DescribeEnvSourceRequest{Slug: slug, Tier: environmentv1.Tier_TIER_PRODUCTION})
			return err
		},
		"SetEnvSourceValue": func() error {
			_, err := variables.SetEnvSourceValue(ctx, &variablestorev1.SetEnvSourceValueRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Coordinate: cell("KEY")})
			return err
		},
	}
	methods := variablestorev1.File_provider_variablestore_v1_variablestore_proto.Services().ByName("VariableStoreService").Methods()
	for index := range methods.Len() {
		if name := string(methods.Get(index).Name()); calls[name] == nil {
			t.Errorf("VariableStoreService declares %s, and the suite never calls it before Configure", name)
		}
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if got := connect.CodeOf(call()); got != connect.CodeFailedPrecondition {
				t.Fatalf("%s before Configure: code = %v, want %v", name, got, connect.CodeFailedPrecondition)
			}
		})
	}
}

func TestAnEnvSourceLogsInWithTheCloudIdentityTheProviderProves(t *testing.T) {
	p := fake.NewProvider(fake.Options{})
	p.WithHooks(func(hooks *provider.Hooks) { hooks.ProveIdentity = p.ProveIdentity })
	variables := variablesServedBy(t, p)
	infisical := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case r.URL.Path == "/api/v1/auth/gcp-auth/login" && body["jwt"] == fake.IDTokenFor("identity-1"):
			_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "token", "expiresIn": 3600})
		case r.URL.Path == "/api/v4/secrets" && r.Header.Get("Authorization") == "Bearer token":
			_ = json.NewEncoder(w).Encode(map[string]any{"secrets": []any{}})
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(infisical.Close)

	_, err := variables.SyncEnvSource(context.Background(), &variablestorev1.SyncEnvSourceRequest{
		Slug: slug,
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		From: &variablestorev1.SyncEnvSourceRequest_EnvSource{EnvSource: &variablestorev1.EnvSource{Kind: &variablestorev1.EnvSource_Infisical{Infisical: &variablestorev1.InfisicalEnvSource{
			Project:     "p-1",
			Environment: "prod",
			Host:        infisical.URL,
			Write:       variablestorev1.WritePolicy_WRITE_POLICY_NEVER,
			Auth:        &variablestorev1.InfisicalAuth{Method: &variablestorev1.InfisicalAuth_Identity{Identity: &variablestorev1.InfisicalIdentityAuth{IdentityId: "identity-1"}}},
		}}}},
	})
	if err != nil {
		t.Fatalf("SyncEnvSource() with identity auth on a provider that proves its cloud identity = %v, want it logged in with the proof the provider gave", err)
	}
}
