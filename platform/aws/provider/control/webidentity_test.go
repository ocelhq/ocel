package control

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAWebIdentityIsWhatTheSDKAssumesTheRoleWithAndTheProcessEnvironmentIsLeftAlone(t *testing.T) {
	for _, name := range credentialEnvVars {
		t.Setenv(name, "")
	}
	t.Setenv(sessionNameVariable, "")
	var asked url.Values
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		asked = r.Form
		w.Header().Set("Content-Type", "text/xml")
		_, _ = io.WriteString(w, `<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials><AccessKeyId>ASIAEXAMPLE</AccessKeyId><SecretAccessKey>secret</SecretAccessKey><SessionToken>token</SessionToken><Expiration>2099-01-01T00:00:00Z</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`)
	}))
	t.Cleanup(stub.Close)
	t.Setenv("AWS_ENDPOINT_URL_STS", stub.URL)
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("a-console-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := &provider.WebIdentity{
		TokenFile:        token,
		IdentityProvider: "arn:aws:iam::123456789012:oidc-provider/console.example/oidc",
		Principal:        "arn:aws:iam::123456789012:role/ocel-production-deploy",
		SessionName:      "ocel-deploy-1",
	}

	p, err := New(context.Background(), provider.Settings{Options: provider.Options{"region": "eu-west-1"}, WebIdentity: identity})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	for _, name := range []string{roleARNVariable, tokenFileVariable, sessionNameVariable} {
		if value := os.Getenv(name); value != "" {
			t.Errorf("New() set %s=%q in the process environment, where every later session reads it as a credential of its own", name, value)
		}
	}
	credentials, err := p.Config.Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("Retrieve() = %v", err)
	}
	if credentials.AccessKeyID != "ASIAEXAMPLE" || asked.Get("Action") != "AssumeRoleWithWebIdentity" ||
		asked.Get("RoleArn") != identity.Principal || asked.Get("WebIdentityToken") != "a-console-token" || asked.Get("RoleSessionName") != identity.SessionName {
		t.Errorf("the SDK asked STS %v and holds %q, want the role assumed with the web identity's token and session", asked, credentials.AccessKeyID)
	}
	if env := p.WebIdentityEnv(); env[roleARNVariable] != identity.Principal || env[tokenFileVariable] != token || env[sessionNameVariable] != identity.SessionName {
		t.Errorf("WebIdentityEnv() = %v, want the role, token file and session the provisioning engine assumes the role with", env)
	}
}

func TestEveryVariableThatNamesACredentialTheSDKWouldPreferIsExplicit(t *testing.T) {
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"} {
		if !slices.Contains(credentialEnvVars, name) {
			t.Errorf("credentialEnvVars = %v, want %s among them", credentialEnvVars, name)
		}
	}
}
