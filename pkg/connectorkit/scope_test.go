package connectorkit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1/envvarsv1connect"
)

const sneakProcedure = "/provider.envvars.v1.EnvVarsService/Sneak"

func TestEveryProcedureTheServiceDeclaresMapsToAScope(t *testing.T) {
	declared := map[string]string{
		envvarsv1connect.EnvVarsServiceSetValueProcedure:       CapabilityEnvVarsWrite,
		envvarsv1connect.EnvVarsServiceDeleteValueProcedure:    CapabilityEnvVarsWrite,
		envvarsv1connect.EnvVarsServiceSetReferenceProcedure:   CapabilityEnvVarsWrite,
		envvarsv1connect.EnvVarsServiceSetBindingProcedure:     CapabilityEnvVarsWrite,
		envvarsv1connect.EnvVarsServiceRemoveBindingProcedure:  CapabilityEnvVarsWrite,
		envvarsv1connect.EnvVarsServiceRevealValuesProcedure:   CapabilityEnvVarsReveal,
		envvarsv1connect.EnvVarsServiceGetValueProcedure:       CapabilityEnvVarsRead,
		envvarsv1connect.EnvVarsServiceListValuesProcedure:     CapabilityEnvVarsRead,
		envvarsv1connect.EnvVarsServiceListReferencesProcedure: CapabilityEnvVarsRead,
		envvarsv1connect.EnvVarsServiceListVersionsProcedure:   CapabilityEnvVarsRead,
		envvarsv1connect.EnvVarsServiceListBindingsProcedure:   CapabilityEnvVarsRead,
	}

	methods := envvarsv1.File_provider_envvars_v1_envvars_proto.Services().ByName("EnvVarsService").Methods()
	for index := range methods.Len() {
		procedure := "/provider.envvars.v1.EnvVarsService/" + string(methods.Get(index).Name())
		want, listed := declared[procedure]
		if !listed {
			t.Fatalf("%s is declared by the service and this test names no scope for it", procedure)
		}
		got, mapped := scopeOf(procedure, nil)
		if !mapped || got != want {
			t.Fatalf("scopeOf(%s) = (%q, %v), want (%q, true)", procedure, got, mapped, want)
		}
	}
}

func TestAnUnlistedProcedureMapsToNoScope(t *testing.T) {
	for _, procedure := range []string{sneakProcedure, "/other.v1.Service/Anything", ""} {
		if got, mapped := scopeOf(procedure, nil); mapped || got != "" {
			t.Fatalf("scopeOf(%q) = (%q, %v), want (\"\", false)", procedure, got, mapped)
		}
	}
}

func signingConsole(t *testing.T) (origin string, token string) {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	signer, err := jwk.Import(private)
	if err != nil {
		t.Fatalf("Import(private) error = %v", err)
	}
	if err := signer.Set(jwk.KeyIDKey, "key-1"); err != nil {
		t.Fatalf("Set(kid) error = %v", err)
	}
	published, err := jwk.Import(public)
	if err != nil {
		t.Fatalf("Import(public) error = %v", err)
	}
	for name, value := range map[string]any{
		jwk.KeyIDKey:     "key-1",
		jwk.AlgorithmKey: jwa.EdDSA().String(),
		jwk.KeyUsageKey:  "sig",
	} {
		if err := published.Set(name, value); err != nil {
			t.Fatalf("Set(%s) error = %v", name, err)
		}
	}
	set := jwk.NewSet()
	if err := set.AddKey(published); err != nil {
		t.Fatalf("AddKey() error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(server.Close)

	claimed, err := jwt.NewBuilder().
		Issuer(server.URL).
		Audience([]string{"conn-shop"}).
		Subject("org:org-shop").
		JwtID("one").
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(60*time.Second)).
		Claim("act", "user-1").
		Claim("scope", []string{CapabilityEnvVarsRead, CapabilityEnvVarsWrite, CapabilityEnvVarsReveal}).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	signed, err := jwt.Sign(claimed, jwt.WithKey(jwa.EdDSA(), signer))
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	return server.URL, string(signed)
}

func TestAnUnlistedProcedureIsDeniedUnderEveryScope(t *testing.T) {
	origin, token := signingConsole(t)

	guard, err := newTrust(Spec{
		Console:        origin,
		ConnectorID:    "conn-shop",
		OrganizationID: "org-shop",
		Grants: []string{
			CapabilityEnvVarsRead,
			CapabilityEnvVarsWrite,
			CapabilityEnvVarsReveal,
		},
	})
	if err != nil {
		t.Fatalf("newTrust() error = %v", err)
	}

	reached := false
	handler := connect.NewUnaryHandler(sneakProcedure,
		func(context.Context, *connect.Request[envvarsv1.ListValuesRequest]) (*connect.Response[envvarsv1.ListValuesResponse], error) {
			reached = true
			return connect.NewResponse(&envvarsv1.ListValuesResponse{}), nil
		},
		connect.WithInterceptors(guard.interceptor()),
	)
	mux := http.NewServeMux()
	mux.Handle(sneakProcedure, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := connect.NewClient[envvarsv1.ListValuesRequest, envvarsv1.ListValuesResponse](
		server.Client(), server.URL+sneakProcedure)
	request := connect.NewRequest(&envvarsv1.ListValuesRequest{})
	request.Header().Set("Authorization", "Bearer "+token)

	_, err = client.CallUnary(context.Background(), request)
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("CallUnary() code = %v, want %v (err = %v)", connect.CodeOf(err), connect.CodePermissionDenied, err)
	}
	if reached {
		t.Fatal("the handler ran for a procedure no scope maps")
	}
}
