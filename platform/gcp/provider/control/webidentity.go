package control

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/provider"
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

func useWebIdentity(identity *provider.WebIdentity) error {
	if identity == nil {
		return nil
	}
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
	path := filepath.Join(filepath.Dir(identity.TokenFile), "google-external-account.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return fmt.Errorf("write the external account that exchanges the web identity token: %w", err)
	}
	return os.Setenv(applicationCredentialsVariable, path)
}
