package connectorserver_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/ocelhq/ocel/pkg/connectorserver"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

const (
	connectorID    = "conn-shop"
	organizationID = "org-shop"
)

type console struct {
	origin string
	signer jwk.Key
}

func consoleServing(t *testing.T) *console {
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
		jwk.AlgorithmKey: jwa.EdDSA(),
	} {
		if err := published.Set(name, value); err != nil {
			t.Fatalf("Set(%s) error = %v", name, err)
		}
	}
	set := jwk.NewSet()
	if err := set.AddKey(published); err != nil {
		t.Fatalf("AddKey() error = %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &console{origin: server.URL, signer: signer}
}

type minted struct {
	audience string
	subject  string
	scope    []string
}

func (c *console) token(t *testing.T, m minted) string {
	t.Helper()

	audience := m.audience
	if audience == "" {
		audience = connectorID
	}
	subject := m.subject
	if subject == "" {
		subject = "org:" + organizationID
	}

	token, err := jwt.NewBuilder().
		Issuer(c.origin).
		Audience([]string{audience}).
		Subject(subject).
		JwtID("one").
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(60*time.Second)).
		Claim("act", "user-1").
		Claim("scope", m.scope).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	signed, err := jwt.Sign(token, jwt.WithKey(jwa.EdDSA(), c.signer))
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	return string(signed)
}

func servedConnector(t *testing.T, at *console, grants []string) *httptest.Server {
	t.Helper()

	mux, err := connectorserver.Mux(connectorserver.Spec{
		Config: connectorserver.Config{
			Console:        at.origin,
			ConnectorID:    connectorID,
			OrganizationID: organizationID,
			Target:         "fake/shop",
			Grants:         grants,
		},
		Version: "test",
		Vendor:  "fake",
		VariableStore: variablestoreserver.Backend{
			KeyValues: fake.NewKeyValues(),
			Cipher:    fake.NewCipher(),
		},
	})
	if err != nil {
		t.Fatalf("Mux() error = %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func connectorServing(t *testing.T, at *console, grants []string, options ...connect.ClientOption) variablestorev1connect.VariableStoreServiceClient {
	t.Helper()

	server := servedConnector(t, at, grants)
	return variablestorev1connect.NewVariableStoreServiceClient(server.Client(), server.URL, options...)
}

func probed(t *testing.T, server *httptest.Server, token string) *http.Response {
	t.Helper()

	request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/capabilities", nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func TestAnUnauthenticatedCapabilitiesProbeIsRefused(t *testing.T) {
	at := consoleServing(t)
	server := servedConnector(t, at, everything())

	if response := probed(t, server, ""); response.StatusCode != http.StatusUnauthorized {
		t.Errorf("an unauthenticated capabilities probe answered %s, want 401", response.Status)
	}
}

func TestACapabilitiesProbeUnderAnotherAccountsTokenIsRefused(t *testing.T) {
	at := consoleServing(t)
	server := servedConnector(t, at, everything())
	token := at.token(t, minted{subject: "org:someone-else", scope: []string{connectorserver.CapabilityVariablesRead}})

	if response := probed(t, server, token); response.StatusCode != http.StatusForbidden {
		t.Errorf("a capabilities probe for another account answered %s, want 403", response.Status)
	}
}

func TestACapabilitiesProbeUnderAConsoleTokenAnswers(t *testing.T) {
	at := consoleServing(t)
	server := servedConnector(t, at, everything())
	token := at.token(t, minted{scope: []string{connectorserver.CapabilityVariablesRead}})

	response := probed(t, server, token)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("a capabilities probe under a console token answered %s, want 200", response.Status)
	}
	var read struct {
		Target       string   `json:"target"`
		Capabilities []string `json:"capabilities"`
	}
	if err := json.NewDecoder(response.Body).Decode(&read); err != nil {
		t.Fatal(err)
	}
	if read.Target != "fake/shop" || len(read.Capabilities) != 3 {
		t.Errorf("the probe answered %+v, want the target and every grant this connector has", read)
	}
}

func bearer(token string) connect.ClientOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(
		func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
				request.Header().Set("Authorization", "Bearer "+token)
				return next(ctx, request)
			}
		},
	))
}

func withBearer(t *testing.T, at *console, grants []string, token string) variablestorev1connect.VariableStoreServiceClient {
	t.Helper()

	return connectorServing(t, at, grants, bearer(token))
}

func everything() []string {
	return []string{
		connectorserver.CapabilityVariablesRead,
		connectorserver.CapabilityVariablesWrite,
		connectorserver.CapabilityVariablesReveal,
	}
}

func TestReadPassesUnderAValidToken(t *testing.T) {
	at := consoleServing(t)
	variables := withBearer(t, at, everything(), at.token(t, minted{scope: []string{connectorserver.CapabilityVariablesRead}}))

	if _, err := variables.ListValues(context.Background(), &variablestorev1.ListValuesRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Slug: "shop",
	}); err != nil {
		t.Fatalf("ListValues() error = %v", err)
	}
}

func TestAMissingTokenIsUnauthenticated(t *testing.T) {
	at := consoleServing(t)
	variables := connectorServing(t, at, everything())

	_, err := variables.ListValues(context.Background(), &variablestorev1.ListValuesRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Slug: "shop",
	})
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("ListValues() code = %v, want %v (err = %v)", connect.CodeOf(err), connect.CodeUnauthenticated, err)
	}
}

func TestAnotherAudienceIsUnauthenticated(t *testing.T) {
	at := consoleServing(t)
	variables := withBearer(t, at, everything(), at.token(t, minted{
		audience: "conn-other",
		scope:    []string{connectorserver.CapabilityVariablesRead},
	}))

	_, err := variables.ListValues(context.Background(), &variablestorev1.ListValuesRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Slug: "shop",
	})
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("ListValues() code = %v, want %v (err = %v)", connect.CodeOf(err), connect.CodeUnauthenticated, err)
	}
}

func TestAnotherAccountIsDenied(t *testing.T) {
	at := consoleServing(t)
	variables := withBearer(t, at, everything(), at.token(t, minted{
		subject: "org:other",
		scope:   []string{connectorserver.CapabilityVariablesRead},
	}))

	_, err := variables.ListValues(context.Background(), &variablestorev1.ListValuesRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Slug: "shop",
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("ListValues() code = %v, want %v (err = %v)", connect.CodeOf(err), connect.CodePermissionDenied, err)
	}
}

func TestWritingWithoutTheScopeIsDenied(t *testing.T) {
	at := consoleServing(t)
	variables := withBearer(t, at, everything(), at.token(t, minted{scope: []string{connectorserver.CapabilityVariablesRead}}))

	_, err := variables.SetValue(context.Background(), &variablestorev1.SetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: &variablestorev1.Coordinate{Slug: "shop", Key: "DATABASE_URL"},
		Value:      "postgres://",
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("SetValue() code = %v, want %v (err = %v)", connect.CodeOf(err), connect.CodePermissionDenied, err)
	}
}

func TestRevealingWithoutTheGrantIsDenied(t *testing.T) {
	at := consoleServing(t)
	grants := []string{connectorserver.CapabilityVariablesRead, connectorserver.CapabilityVariablesWrite}
	variables := withBearer(t, at, grants, at.token(t, minted{scope: everything()}))

	_, err := variables.RevealValues(context.Background(), &variablestorev1.RevealValuesRequest{
		Tier:  environmentv1.Tier_TIER_PRODUCTION,
		Slug:  "shop",
		Cells: []*variablestorev1.Coordinate{{Slug: "shop", Key: "DATABASE_URL"}},
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("RevealValues() code = %v, want %v (err = %v)", connect.CodeOf(err), connect.CodePermissionDenied, err)
	}
}

func TestGetValueAsksForRevealOnlyWhenItReveals(t *testing.T) {
	at := consoleServing(t)
	reading := at.token(t, minted{scope: []string{connectorserver.CapabilityVariablesRead}})

	plain := withBearer(t, at, everything(), reading)
	if _, err := plain.GetValue(context.Background(), &variablestorev1.GetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: &variablestorev1.Coordinate{Slug: "shop", Key: "DATABASE_URL"},
	}); connect.CodeOf(err) == connect.CodePermissionDenied {
		t.Fatalf("GetValue() without reveal = %v, want it past the guard", err)
	}

	_, err := plain.GetValue(context.Background(), &variablestorev1.GetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: &variablestorev1.Coordinate{Slug: "shop", Key: "DATABASE_URL"},
		Reveal:     true,
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("GetValue(reveal) code = %v, want %v (err = %v)", connect.CodeOf(err), connect.CodePermissionDenied, err)
	}

	revealing := withBearer(t, at, everything(), at.token(t, minted{scope: everything()}))
	if _, err := revealing.GetValue(context.Background(), &variablestorev1.GetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: &variablestorev1.Coordinate{Slug: "shop", Key: "DATABASE_URL"},
		Reveal:     true,
	}); connect.CodeOf(err) == connect.CodePermissionDenied {
		t.Fatalf("GetValue(reveal) under the reveal scope = %v, want it past the guard", err)
	}
}

func TestAConnectorSyncsOnlyTheEnvSourceADeployRegistered(t *testing.T) {
	at := consoleServing(t)
	variables := withBearer(t, at, everything(), at.token(t, minted{scope: []string{connectorserver.CapabilityVariablesWrite}}))

	synced, err := variables.SyncEnvSource(context.Background(), &variablestorev1.SyncEnvSourceRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Slug: "shop",
		From: &variablestorev1.SyncEnvSourceRequest_Registered{Registered: &variablestorev1.RegisteredEnvSource{}},
	})
	if err != nil || synced.GetStatus().GetEnvSource() != "builtin" {
		t.Fatalf("SyncEnvSource(registered) under the write scope = %+v, %v, want it synced", synced, err)
	}

	_, err = variables.SyncEnvSource(context.Background(), &variablestorev1.SyncEnvSourceRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Slug: "shop",
		From: &variablestorev1.SyncEnvSourceRequest_EnvSource{EnvSource: &variablestorev1.EnvSource{Kind: &variablestorev1.EnvSource_Infisical{Infisical: &variablestorev1.InfisicalEnvSource{
			Project:     "p-1",
			Environment: "prod",
			Host:        "https://infisical.example.com",
			Write:       variablestorev1.WritePolicy_WRITE_POLICY_NEVER,
			Auth: &variablestorev1.InfisicalAuth{Method: &variablestorev1.InfisicalAuth_Universal{Universal: &variablestorev1.InfisicalUniversalAuth{
				ClientIdVariable:     "INFISICAL_CLIENT_ID",
				ClientSecretVariable: "STRIPE_KEY",
			}}},
		}}}},
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "deploy") {
		t.Fatalf("SyncEnvSource() naming an env source through a connector = %v, want it refused pointing at a deploy", err)
	}
}

func saying(t *testing.T, run func()) string {
	t.Helper()

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	stdout := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = stdout }()

	run()

	if err := write.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	said, err := io.ReadAll(read)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	return string(said)
}

func TestEveryRequestSaysWhoAskedAndHowItWent(t *testing.T) {
	at := consoleServing(t)
	variables := withBearer(t, at, everything(), at.token(t, minted{scope: []string{connectorserver.CapabilityVariablesRead}}))

	said := saying(t, func() {
		if _, err := variables.ListValues(context.Background(), &variablestorev1.ListValuesRequest{
			Tier: environmentv1.Tier_TIER_PRODUCTION,
			Slug: "shop",
		}); err != nil {
			t.Fatalf("ListValues() error = %v", err)
		}
		_, _ = variables.SetValue(context.Background(), &variablestorev1.SetValueRequest{
			Tier:       environmentv1.Tier_TIER_PRODUCTION,
			Coordinate: &variablestorev1.Coordinate{Slug: "shop", Key: "TOKEN"},
			Value:      "denied",
		})
	})

	want := []string{
		"act=user-1 org=" + organizationID + " procedure=" + variablestorev1connect.VariableStoreServiceListValuesProcedure + " outcome=pass\n",
		"act=user-1 org=" + organizationID + " procedure=" + variablestorev1connect.VariableStoreServiceSetValueProcedure + " outcome=denied\n",
	}
	for _, line := range want {
		if !strings.Contains(said, line) {
			t.Fatalf("the connector said %q, which does not contain %q", said, line)
		}
	}
}
