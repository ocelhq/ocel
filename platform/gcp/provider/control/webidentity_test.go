package control

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAWebIdentityBecomesTheApplicationDefaultCredentialsExchangingTheTokenFileForTheServiceAccount(t *testing.T) {
	t.Setenv(applicationCredentialsVariable, "")
	dir := t.TempDir()
	identity := &provider.WebIdentity{
		TokenFile:        filepath.Join(dir, "token"),
		IdentityProvider: "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/ocel/providers/console",
		Principal:        "ocel-deploy-production@acme-prod.iam.gserviceaccount.com",
	}

	if err := useWebIdentity(identity); err != nil {
		t.Fatalf("useWebIdentity() = %v", err)
	}

	path := os.Getenv(applicationCredentialsVariable)
	if filepath.Dir(path) != dir {
		t.Fatalf("%s = %q, want a file beside the token, which goes when the token does", applicationCredentialsVariable, path)
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

func TestEveryVariableThatNamesACredentialPulumiGCPWouldPreferIsExplicit(t *testing.T) {
	for _, name := range []string{"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CREDENTIALS", "GOOGLE_CLOUD_KEYFILE_JSON", "GCLOUD_KEYFILE_JSON", "GOOGLE_OAUTH_ACCESS_TOKEN", "GOOGLE_IMPERSONATE_SERVICE_ACCOUNT", "CLOUDSDK_AUTH_ACCESS_TOKEN_FILE"} {
		if !slices.Contains(credentialEnvVars, name) {
			t.Errorf("credentialEnvVars = %v, want %s among them", credentialEnvVars, name)
		}
	}
}
