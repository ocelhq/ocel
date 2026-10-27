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

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
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
