package envidentity_test

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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/envsource"
	"github.com/ocelhq/ocel/platform/gcp/provider/envidentity"
)

var _ envsource.IDTokenIssuer = envidentity.Issuer{}

func isolated(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLOUDSDK_CONFIG", filepath.Join(home, "gcloud"))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("GCE_METADATA_HOST", "")
	t.Setenv("GCE_METADATA_IP", "")
	return home
}

func claimsOf(t *testing.T, assertion string) map[string]any {
	t.Helper()
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		t.Fatalf("assertion %q is no JWT", assertion)
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode the assertion: %v", err)
	}
	claims := map[string]any{}
	if err := json.Unmarshal(body, &claims); err != nil {
		t.Fatalf("decode the assertion claims: %v", err)
	}
	return claims
}

func serviceAccountKey(t *testing.T, dir, tokenURI string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: must(x509.MarshalPKCS8PrivateKey(key))})
	file, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "acme-prod",
		"private_key_id": "k1",
		"private_key":    string(encoded),
		"client_email":   "deployer@acme-prod.iam.gserviceaccount.com",
		"client_id":      "1",
		"token_uri":      tokenURI,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "key.json")
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func idToken(claims map[string]any) string {
	encode := func(part any) string { return base64.RawURLEncoding.EncodeToString(must(json.Marshal(part))) }
	return encode(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." + encode(claims) + ".c2lnbmVk"
}

func TestAServiceAccountKeyMintsATokenForTheIdentityInfisicalNames(t *testing.T) {
	home := isolated(t)
	minted := idToken(map[string]any{"aud": "identity-1234", "email": "deployer@acme-prod.iam.gserviceaccount.com", "exp": time.Now().Add(time.Hour).Unix()})
	var mu sync.Mutex
	var audience string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse the token request: %v", err)
		}
		mu.Lock()
		audience, _ = claimsOf(t, r.PostForm.Get("assertion"))["target_audience"].(string)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": minted})
	}))
	defer server.Close()
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", serviceAccountKey(t, home, server.URL))

	token, err := envidentity.Issuer{}.IDToken(context.Background(), "identity-1234")
	if err != nil {
		t.Fatalf("IDToken() = %v", err)
	}
	if token != minted {
		t.Errorf("IDToken() = %q, want the id_token Google answered", token)
	}
	mu.Lock()
	defer mu.Unlock()
	if audience != "identity-1234" {
		t.Errorf("asked Google for an audience of %q, want the identity id Infisical checks the token's aud against", audience)
	}
}

func TestTheMetadataServerMintsAFullTokenForTheIdentityInfisicalNames(t *testing.T) {
	isolated(t)
	var mu sync.Mutex
	var asked, flavor string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/identity") {
			asked, flavor = r.URL.RawQuery, r.Header.Get("Metadata-Flavor")
		}
		w.Header().Set("Metadata-Flavor", "Google")
		_, _ = w.Write([]byte("minted-by-metadata"))
	}))
	defer server.Close()
	t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(server.URL, "http://"))

	token, err := envidentity.Issuer{}.IDToken(context.Background(), "identity-1234")
	if err != nil {
		t.Fatalf("IDToken() = %v", err)
	}
	if token != "minted-by-metadata" {
		t.Errorf("IDToken() = %q, want the token the metadata server answered", token)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(asked, "audience=identity-1234") || !strings.Contains(asked, "format=full") {
		t.Errorf("asked the metadata server for %q, want audience=identity-1234 and format=full", asked)
	}
	if flavor != "Google" {
		t.Errorf("Metadata-Flavor = %q, want Google", flavor)
	}
}

func TestAUserLoginIsRefusedWithWhatToDeployAsInstead(t *testing.T) {
	home := isolated(t)
	path := filepath.Join(home, "user.json")
	if err := os.WriteFile(path, []byte(`{"type":"authorized_user","client_id":"c","client_secret":"s","refresh_token":"r"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)

	_, err := envidentity.Issuer{}.IDToken(context.Background(), "identity-1234")
	if err == nil || !strings.Contains(err.Error(), "service account") {
		t.Fatalf("IDToken() under a user login = %v, want a refusal naming a service account to deploy as", err)
	}
}
