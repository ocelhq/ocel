package gcloud_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func TestTheApplicationDefaultTokenCarriesTheEmailItsPrincipalIsNamedBy(t *testing.T) {
	var scopes atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		parts := strings.Split(r.PostForm.Get("assertion"), ".")
		if len(parts) == 3 {
			payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var claims struct {
				Scope string `json:"scope"`
			}
			_ = json.Unmarshal(payload, &claims)
			scopes.Store(strings.Fields(claims.Scope))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"ya29.minted","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(server.Close)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "acme-prod",
		"private_key_id": "1",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "deployer@acme-prod.iam.gserviceaccount.com",
		"token_uri":      server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, credentials, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)

	if _, err := (gcloud.ApplicationDefault{}).Token(context.Background()); err != nil {
		t.Fatalf("Token() = %v", err)
	}
	asked, _ := scopes.Load().([]string)
	if !slices.Contains(asked, "https://www.googleapis.com/auth/userinfo.email") {
		t.Errorf("the token was asked for scopes %q; without userinfo.email Google's tokeninfo names no email for a service account's token, and ocel grants its roles to nobody", asked)
	}
}

func TestReadingWhoATokenNamesIsASpanWithGooglesAnswer(t *testing.T) {
	ctx, spans := tracingtest.Record(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		googletest.WriteBody(w, map[string]string{"email": "deploy@acme.iam.gserviceaccount.com"})
	}))
	t.Cleanup(server.Close)

	if _, err := gcloud.PrincipalNamed(ctx, server.URL, "ya29.stub"); err != nil {
		t.Fatal(err)
	}

	asked := spans.Only(t)
	if asked.Name() != "GET "+server.Listener.Addr().String() {
		t.Fatalf("reading the principal made span %q, want the GET it sent", asked.Name())
	}
	if answered := tracingtest.Attributes(asked)["http.response.status_code"]; answered.AsInt64() != http.StatusOK {
		t.Errorf("the span recorded status %d, want %d", answered.AsInt64(), http.StatusOK)
	}
}

func TestARefusedTokenLeavesGooglesAnswerOutOfTheTrace(t *testing.T) {
	const answer = `{"error":"invalid_token","error_description":"ya29.refused belongs to deploy@acme.iam.gserviceaccount.com"}`
	googletest.ServeAsGoogle(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(answer))
	})
	ctx, spans := tracingtest.Record(t)

	if _, err := gcloud.PrincipalNamed(ctx, gcloud.TokenInfoURL, "ya29.refused"); err == nil {
		t.Fatal("PrincipalNamed() = nil, want the refusal")
	}

	asked := spans.Only(t)
	if exported := tracingtest.ExportedText(asked); strings.Contains(exported, "deploy@acme") {
		t.Errorf("trace carries Google's answer about the token:\n%s", exported)
	}
}
