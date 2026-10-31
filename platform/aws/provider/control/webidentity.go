package control

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"

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

func webIdentityCredentials(cfg aws.Config, identity *provider.WebIdentity) aws.CredentialsProvider {
	assumed := stscreds.NewWebIdentityRoleProvider(sts.NewFromConfig(cfg), identity.Principal, stscreds.IdentityTokenFile(identity.TokenFile),
		func(o *stscreds.WebIdentityRoleOptions) { o.RoleSessionName = identity.SessionName })
	return aws.NewCredentialsCache(assumed)
}

func (p *Provider) WebIdentityEnv() map[string]string {
	if p.WebIdentity == nil {
		return nil
	}
	env := map[string]string{roleARNVariable: p.WebIdentity.Principal, tokenFileVariable: p.WebIdentity.TokenFile}
	if p.WebIdentity.SessionName != "" {
		env[sessionNameVariable] = p.WebIdentity.SessionName
	}
	return env
}
