package ports

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
	pubsub "google.golang.org/api/pubsub/v1"
)

func withCredentials(t *testing.T, credentials map[string]string) {
	t.Helper()
	raw, err := json.Marshal(credentials)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "adc.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
	t.Setenv(quotaProjectEnvVar, "")
}

func userCredentials(t *testing.T) {
	t.Helper()
	withCredentials(t, map[string]string{
		"type":             userCredential,
		"client_id":        "id.apps.googleusercontent.com",
		"client_secret":    "secret",
		"refresh_token":    "refresh",
		"quota_project_id": "elsewhere",
	})
}

func TestAUserCredentialBillsItsCallsToTheProjectItDeploysTo(t *testing.T) {
	userCredentials(t)
	var billed string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		billed = r.Header.Get(QuotaProjectHeader)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"projects/acme-prod/topics/orders"}`))
	}))
	defer server.Close()

	options := append(QuotaProject("acme-prod"),
		option.WithEndpoint(server.URL),
		option.WithTokenSource(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "ya29.stub"})))
	service, err := pubsub.NewService(context.Background(), options...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Projects.Topics.Get("projects/acme-prod/topics/orders").Do(); err != nil {
		t.Fatal(err)
	}

	if billed != "acme-prod" {
		t.Errorf("the call was billed to %q, want acme-prod: a user's credential names its own quota project, and a deploy's calls are the target project's", billed)
	}
}

func TestAUserCredentialBillsARawCallToTheProjectTheResourceIsIn(t *testing.T) {
	userCredentials(t)

	if got := QuotaProjectOf("projects/acme-prod/locations/europe-west1/instances/cache"); got != "acme-prod" {
		t.Errorf("QuotaProjectOf() = %q, want acme-prod", got)
	}
	if got := QuotaProjectOf("operations/abc"); got != "" {
		t.Errorf("QuotaProjectOf() of a name in no project = %q, want none", got)
	}
}

func TestAServiceAccountCredentialNamesNoQuotaProject(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	withCredentials(t, map[string]string{
		"type":           "service_account",
		"project_id":     "acme-prod",
		"client_email":   "deploy@acme-prod.iam.gserviceaccount.com",
		"private_key_id": "k1",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
	})

	if got := QuotaProject("acme-prod"); len(got) != 0 {
		t.Errorf("QuotaProject() = %v, want none: naming one takes serviceusage.services.use, which a deploy service account is not granted", got)
	}
}
