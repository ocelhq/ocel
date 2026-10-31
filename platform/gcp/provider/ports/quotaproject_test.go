package ports

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/api/option"
	pubsub "google.golang.org/api/pubsub/v1"

	"github.com/ocelhq/ocel/pkg/tracing"
)

const quotaProjectHeader = "X-Goog-User-Project"

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
	t.Setenv(QuotaProjectEnvVar, "")
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

func serviceAccountCredentials(t *testing.T) {
	t.Helper()
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
}

func billedProjectOf(t *testing.T, quotaProject string) string {
	t.Helper()
	var billed string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"ya29.stub","token_type":"Bearer","expires_in":3600}`))
			return
		}
		billed = r.Header.Get(quotaProjectHeader)
		_, _ = w.Write([]byte(`{"name":"projects/acme-prod/topics/orders"}`))
	}))
	defer server.Close()
	redirected := tracing.NewBaseTransport()
	redirected.Proxy = nil
	redirected.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	redirected.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	original := http.DefaultTransport
	http.DefaultTransport = redirected
	defer func() { http.DefaultTransport = original }()

	options, err := newRESTOptions(context.Background(), Credentials{}, "", quotaProject)
	if err != nil {
		t.Fatal(err)
	}
	service, err := pubsub.NewService(context.Background(), append(options, option.WithEndpoint(server.URL))...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Projects.Topics.Get("projects/acme-prod/topics/orders").Do(); err != nil {
		t.Fatal(err)
	}
	return billed
}

func TestAUserCredentialBillsItsCallsToTheProjectItDeploysTo(t *testing.T) {
	userCredentials(t)

	if billed := billedProjectOf(t, findQuotaProject("acme-prod", Credentials{})); billed != "acme-prod" {
		t.Errorf("the call was billed to %q, want acme-prod: a user's credential names its own quota project, and a deploy's calls are the target project's", billed)
	}
}

func TestAQuotaProjectTheEnvironmentNamesBillsTheCallsInsteadOfTheProjectDeployedTo(t *testing.T) {
	userCredentials(t)
	t.Setenv(QuotaProjectEnvVar, "acme-billing")

	if billed := billedProjectOf(t, findQuotaProject("acme-prod", Credentials{})); billed != "acme-billing" {
		t.Errorf("the call was billed to %q, want acme-billing: %s names the project a user bills Google API calls to", billed, QuotaProjectEnvVar)
	}
}

func TestAServiceAccountCredentialNamesNoQuotaProject(t *testing.T) {
	serviceAccountCredentials(t)

	if got := findQuotaProject("acme-prod", Credentials{}); got != "" {
		t.Errorf("findQuotaProject() = %q, want none: a service account's own project bills its calls, as Google's libraries default it", got)
	}
}

func TestClientsReadTheCredentialOnceForTheQuotaProjectOfEveryCall(t *testing.T) {
	userCredentials(t)
	clients := &Clients{Project: "acme-prod", Region: "europe-west1"}
	if got := clients.QuotaProject(); got != "acme-prod" {
		t.Fatalf("QuotaProject() = %q, want acme-prod", got)
	}

	serviceAccountCredentials(t)

	if got := clients.QuotaProject(); got != "acme-prod" {
		t.Errorf("QuotaProject() = %q after the credential file changed, want acme-prod: the credential was read again for a later call", got)
	}
}

func TestClientsOfAnEmulatorNameNoQuotaProject(t *testing.T) {
	userCredentials(t)
	clients := &Clients{Project: "acme-prod", Region: "europe-west1", Endpoint: "http://127.0.0.1:1"}

	if got := clients.QuotaProject(); got != "" {
		t.Errorf("QuotaProject() = %q, want none: an emulator bills no one", got)
	}
}
