package control

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAWebIdentityBecomesTheProvidersOwnCredentialsAndTheEnginesExternalAccountWithoutTouchingTheEnvironment(t *testing.T) {
	t.Setenv(applicationCredentialsVariable, "")
	dir := t.TempDir()
	identity := &provider.WebIdentity{
		TokenFile:        filepath.Join(dir, "token"),
		IdentityProvider: "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/ocel/providers/console",
		Principal:        "ocel-deploy-production@acme-prod.iam.gserviceaccount.com",
	}

	p, err := New(context.Background(), provider.Settings{Options: provider.Options{"project": "acme-prod", "region": "europe-west1"}, WebIdentity: identity})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	if named := os.Getenv(applicationCredentialsVariable); named != "" {
		t.Errorf("%s = %q after New, want it untouched: a credential in the environment reaches every later session in this process", applicationCredentialsVariable, named)
	}
	found, err := p.GoogleCredentials.Find(context.Background())
	if err != nil {
		t.Fatalf("GoogleCredentials.Find() = %v", err)
	}
	var account struct {
		Type     string `json:"type"`
		Audience string `json:"audience"`
	}
	if err := json.Unmarshal(found.JSON, &account); err != nil || account.Type != "external_account" || account.Audience != identity.IdentityProvider {
		t.Errorf("the provider's credentials = %s, want the external account exchanging the web identity", found.JSON)
	}
	path := p.WebIdentityEnv()[applicationCredentialsVariable]
	if filepath.Dir(path) != dir {
		t.Fatalf("WebIdentityEnv()[%s] = %q, want a file beside the token, which goes when the token does", applicationCredentialsVariable, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the credential file's mode = %v, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var written struct {
		Type                           string `json:"type"`
		Audience                       string `json:"audience"`
		SubjectTokenType               string `json:"subject_token_type"`
		TokenURL                       string `json:"token_url"`
		ServiceAccountImpersonationURL string `json:"service_account_impersonation_url"`
		CredentialSource               struct {
			File string `json:"file"`
		} `json:"credential_source"`
	}
	if err := json.Unmarshal(data, &written); err != nil {
		t.Fatal(err)
	}
	if written.Type != "external_account" || written.Audience != identity.IdentityProvider ||
		written.SubjectTokenType != "urn:ietf:params:oauth:token-type:jwt" || written.TokenURL != "https://sts.googleapis.com/v1/token" ||
		written.CredentialSource.File != identity.TokenFile {
		t.Errorf("the credential file = %s, want an external account exchanging the token file at the identity provider", data)
	}
	if want := "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/ocel-deploy-production@acme-prod.iam.gserviceaccount.com:generateAccessToken"; written.ServiceAccountImpersonationURL != want {
		t.Errorf("service_account_impersonation_url = %q, want %q", written.ServiceAccountImpersonationURL, want)
	}
}

func TestAProviderHandedNoWebIdentityNamesNoCredentialToItsEngine(t *testing.T) {
	p, err := New(context.Background(), provider.Settings{Options: provider.Options{"project": "acme-prod", "region": "europe-west1"}})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if env := p.WebIdentityEnv(); env != nil {
		t.Errorf("WebIdentityEnv() = %v, want none: the engine finds the credentials its environment names", env)
	}
}

func TestEveryVariableThatNamesACredentialPulumiGCPWouldPreferIsExplicit(t *testing.T) {
	for _, name := range []string{"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CREDENTIALS", "GOOGLE_CLOUD_KEYFILE_JSON", "GCLOUD_KEYFILE_JSON", "GOOGLE_OAUTH_ACCESS_TOKEN", "GOOGLE_IMPERSONATE_SERVICE_ACCOUNT", "CLOUDSDK_AUTH_ACCESS_TOKEN_FILE"} {
		if !slices.Contains(credentialEnvVars, name) {
			t.Errorf("credentialEnvVars = %v, want %s among them", credentialEnvVars, name)
		}
	}
}
