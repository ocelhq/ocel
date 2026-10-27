package aws

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/config"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAWebIdentityIsWhatTheSDKAssumesTheRoleWith(t *testing.T) {
	for _, name := range []string{roleARNVariable, tokenFileVariable, sessionNameVariable} {
		t.Setenv(name, "")
	}
	token := filepath.Join(t.TempDir(), "token")
	identity := &provider.WebIdentity{
		TokenFile:        token,
		IdentityProvider: "arn:aws:iam::123456789012:oidc-provider/console.example/oidc",
		Principal:        "arn:aws:iam::123456789012:role/ocel-production-deploy",
		SessionName:      "ocel-deploy-1",
	}

	if err := useWebIdentity(identity); err != nil {
		t.Fatalf("useWebIdentity() = %v", err)
	}

	env, err := config.NewEnvConfig()
	if err != nil {
		t.Fatal(err)
	}
	if env.RoleARN != identity.Principal || env.WebIdentityTokenFilePath != token || env.RoleSessionName != "ocel-deploy-1" {
		t.Errorf("the SDK reads role %q, token file %q, session %q from the environment, want %q, %q, %q",
			env.RoleARN, env.WebIdentityTokenFilePath, env.RoleSessionName, identity.Principal, token, identity.SessionName)
	}
	if _, err := config.LoadDefaultConfig(context.Background(), config.WithRegion("eu-west-1")); err != nil {
		t.Errorf("LoadDefaultConfig() = %v, want the web identity to load", err)
	}
	if os.Getenv(sessionNameVariable) == "" {
		t.Error("the session name was not handed on")
	}
}

func TestEveryVariableThatNamesACredentialTheSDKWouldPreferIsExplicit(t *testing.T) {
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"} {
		if !slices.Contains(credentialEnvVars, name) {
			t.Errorf("credentialEnvVars = %v, want %s among them", credentialEnvVars, name)
		}
	}
}
