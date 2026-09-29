package connectorserver

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

	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
)

const sneakProcedure = "/provider.variablestore.v1.VariableStoreService/Sneak"

func TestEveryProcedureTheServiceDeclaresMapsToAScope(t *testing.T) {
	declared := map[string]string{
		variablestorev1connect.VariableStoreServiceSetValueProcedure:          CapabilityVariablesWrite,
		variablestorev1connect.VariableStoreServiceDeleteValueProcedure:       CapabilityVariablesWrite,
		variablestorev1connect.VariableStoreServiceSetReferenceProcedure:      CapabilityVariablesWrite,
		variablestorev1connect.VariableStoreServiceSetBindingProcedure:        CapabilityVariablesWrite,
		variablestorev1connect.VariableStoreServiceRemoveBindingProcedure:     CapabilityVariablesWrite,
		variablestorev1connect.VariableStoreServiceSyncEnvSourceProcedure:     CapabilityVariablesWrite,
		variablestorev1connect.VariableStoreServiceSetEnvSourceValueProcedure: CapabilityVariablesWrite,
		variablestorev1connect.VariableStoreServiceRevealValuesProcedure:      CapabilityVariablesReveal,
		variablestorev1connect.VariableStoreServiceGetValueProcedure:          CapabilityVariablesRead,
		variablestorev1connect.VariableStoreServiceListValuesProcedure:        CapabilityVariablesRead,
		variablestorev1connect.VariableStoreServiceListReferencesProcedure:    CapabilityVariablesRead,
		variablestorev1connect.VariableStoreServiceListVersionsProcedure:      CapabilityVariablesRead,
		variablestorev1connect.VariableStoreServiceListBindingsProcedure:      CapabilityVariablesRead,
		variablestorev1connect.VariableStoreServiceDescribeEnvSourceProcedure: CapabilityVariablesRead,
	}

	methods := variablestorev1.File_provider_variablestore_v1_variablestore_proto.Services().ByName("VariableStoreService").Methods()
	for index := range methods.Len() {
		procedure := "/provider.variablestore.v1.VariableStoreService/" + string(methods.Get(index).Name())
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

func TestSyncingAnEnvSourceAsksForWriteWhateverItNames(t *testing.T) {
	for name, req := range map[string]*variablestorev1.SyncEnvSourceRequest{
		"registered": {From: &variablestorev1.SyncEnvSourceRequest_Registered{Registered: &variablestorev1.RegisteredEnvSource{}}},
		"infisical":  {From: &variablestorev1.SyncEnvSourceRequest_EnvSource{EnvSource: &variablestorev1.EnvSource{Kind: &variablestorev1.EnvSource_Infisical{Infisical: &variablestorev1.InfisicalEnvSource{}}}}},
	} {
		got, mapped := scopeOf(variablestorev1connect.VariableStoreServiceSyncEnvSourceProcedure, req)
		if !mapped || got != CapabilityVariablesWrite {
			t.Errorf("scopeOf(SyncEnvSource, %s) = (%q, %v), want (%q, true): a connector syncs only what a deploy registered, so no sync sends anything a caller named", name, got, mapped, CapabilityVariablesWrite)
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
		Claim("scope", []string{CapabilityVariablesRead, CapabilityVariablesWrite, CapabilityVariablesReveal}).
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

	guard, err := newTrust(Spec{Config: Config{
		Console:        origin,
		ConnectorID:    "conn-shop",
		OrganizationID: "org-shop",
		Grants: []string{
			CapabilityVariablesRead,
			CapabilityVariablesWrite,
			CapabilityVariablesReveal,
		},
	}})
	if err != nil {
		t.Fatalf("newTrust() error = %v", err)
	}

	reached := false
	handler := connect.NewUnaryHandler(sneakProcedure,
		func(context.Context, *connect.Request[variablestorev1.ListValuesRequest]) (*connect.Response[variablestorev1.ListValuesResponse], error) {
			reached = true
			return connect.NewResponse(&variablestorev1.ListValuesResponse{}), nil
		},
		connect.WithInterceptors(guard.interceptor()),
	)
	mux := http.NewServeMux()
	mux.Handle(sneakProcedure, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := connect.NewClient[variablestorev1.ListValuesRequest, variablestorev1.ListValuesResponse](
		server.Client(), server.URL+sneakProcedure)
	request := connect.NewRequest(&variablestorev1.ListValuesRequest{})
	request.Header().Set("Authorization", "Bearer "+token)

	_, err = client.CallUnary(context.Background(), request)
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("CallUnary() code = %v, want %v (err = %v)", connect.CodeOf(err), connect.CodePermissionDenied, err)
	}
	if reached {
		t.Fatal("the handler ran for a procedure no scope maps")
	}
}
