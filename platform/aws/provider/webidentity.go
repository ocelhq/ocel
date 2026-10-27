package aws

import (
	"os"

	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	roleARNVariable     = "AWS_ROLE_ARN"
	tokenFileVariable   = "AWS_WEB_IDENTITY_TOKEN_FILE"
	sessionNameVariable = "AWS_ROLE_SESSION_NAME"
)

var credentialEnvVars = []string{
	"AWS_ACCESS_KEY_ID",
	"AWS_PROFILE",
	"AWS_DEFAULT_PROFILE",
	roleARNVariable,
	tokenFileVariable,
	"AWS_CONTAINER_CREDENTIALS_FULL_URI",
	"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
}

func useWebIdentity(identity *provider.WebIdentity) error {
	if identity == nil {
		return nil
	}
	if err := os.Setenv(roleARNVariable, identity.Principal); err != nil {
		return err
	}
	if err := os.Setenv(tokenFileVariable, identity.TokenFile); err != nil {
		return err
	}
	if identity.SessionName == "" {
		return os.Unsetenv(sessionNameVariable)
	}
	return os.Setenv(sessionNameVariable, identity.SessionName)
}
