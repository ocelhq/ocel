package ports_test

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

	"google.golang.org/api/option"
	pubsub "google.golang.org/api/pubsub/v1"

	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
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

	if _, err := (ports.Credentials{}).Token(context.Background()); err != nil {
		t.Fatalf("Token() = %v", err)
	}
	asked, _ := scopes.Load().([]string)
	if !slices.Contains(asked, "https://www.googleapis.com/auth/userinfo.email") {
		t.Errorf("the token was asked for scopes %q; without userinfo.email Google's tokeninfo names no email for a service account's token, and ocel grants its roles to nobody", asked)
	}
}

func TestAClientOpenedWithAnExternalAccountAuthenticatesAsItAndNotAsTheApplicationDefault(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-such-key.json"))
	var authorization atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"ya29.federated","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":3600}`))
			return
		}
		authorization.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"name":"projects/acme-prod/topics/orders"}`))
	}))
	t.Cleanup(server.Close)
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("a-console-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	externalAccount, err := json.Marshal(map[string]any{
		"type":               "external_account",
		"audience":           "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/ocel/providers/console",
		"subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
		"token_url":          server.URL + "/token",
		"credential_source":  map[string]string{"file": token},
	})
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := ports.NewExternalAccountCredentials(context.Background(), externalAccount)
	if err != nil {
		t.Fatalf("NewExternalAccountCredentials() = %v", err)
	}

	service, err := ports.NewRESTService(context.Background(), credentials, pubsub.NewService, "", "", option.WithEndpoint(server.URL))
	if err != nil {
		t.Fatalf("NewRESTService() = %v", err)
	}
	if _, err := service.Projects.Topics.Get("projects/acme-prod/topics/orders").Do(); err != nil {
		t.Fatalf("Topics.Get() = %v", err)
	}
	if got := authorization.Load(); got != "Bearer ya29.federated" {
		t.Errorf("the call was authorized with %q, want the token the external account exchanged its web identity for", got)
	}
}
