package control

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const applicationCredentialsVariable = "GOOGLE_APPLICATION_CREDENTIALS"

var credentialEnvVars = []string{
	applicationCredentialsVariable,
	"GOOGLE_GHA_CREDS_PATH",
	"CLOUDSDK_AUTH_ACCESS_TOKEN_FILE",
	"CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE",
	"GOOGLE_CREDENTIALS",
	"GOOGLE_CLOUD_KEYFILE_JSON",
	"GCLOUD_KEYFILE_JSON",
	"GOOGLE_OAUTH_ACCESS_TOKEN",
	"GOOGLE_IMPERSONATE_SERVICE_ACCOUNT",
}

type externalAccount struct {
	Type                           string           `json:"type"`
	Audience                       string           `json:"audience"`
	SubjectTokenType               string           `json:"subject_token_type"`
	TokenURL                       string           `json:"token_url"`
	ServiceAccountImpersonationURL string           `json:"service_account_impersonation_url"`
	CredentialSource               credentialSource `json:"credential_source"`
}

type credentialSource struct {
	File string `json:"file"`
}

func (p *Provider) useWebIdentity(ctx context.Context, identity *provider.WebIdentity) error {
	encoded, err := json.Marshal(externalAccount{
		Type:                           "external_account",
		Audience:                       identity.IdentityProvider,
		SubjectTokenType:               "urn:ietf:params:oauth:token-type:jwt",
		TokenURL:                       "https://sts.googleapis.com/v1/token",
		ServiceAccountImpersonationURL: "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/" + identity.Principal + ":generateAccessToken",
		CredentialSource:               credentialSource{File: identity.TokenFile},
	})
	if err != nil {
		return err
	}
	credentials, err := ports.NewExternalAccountCredentials(ctx, encoded)
	if err != nil {
		return fmt.Errorf("read the external account that exchanges the web identity token: %w", err)
	}
	path := filepath.Join(filepath.Dir(identity.TokenFile), "google-external-account.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return fmt.Errorf("write the external account that exchanges the web identity token: %w", err)
	}
	p.GoogleCredentials = credentials
	p.Tokens = credentials
	p.externalAccountFile = path
	return nil
}

func (p *Provider) WebIdentityEnv() map[string]string {
	if p.externalAccountFile == "" {
		return nil
	}
	return map[string]string{applicationCredentialsVariable: p.externalAccountFile}
}
