package cloudflare

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/accounts"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const credentialHeading = "Cloudflare API token"

var accountPermissions = []string{
	"Account · Workers Scripts · Edit",
	"Account · Workers R2 Storage · Edit",
	"Account · Account Settings · Read",
	"Account · Billing · Read",
}

var tokenMinting = []string{
	"User · API Tokens · Edit",
}

var zonePermissions = []string{
	"Zone · Zone · Read",
	"Zone · DNS · Edit",
	"Zone · SSL and Certificates · Read",
	"Zone · Workers Routes · Edit",
}

func bootstrapPermissions() []string {
	return slices.Concat(accountPermissions, tokenMinting, zonePermissions)
}

func deployPermissions() []string {
	return slices.Concat(accountPermissions, zonePermissions)
}

func credentialPermissions(tier edge.CredentialTier) (edge.CredentialDocument, error) {
	var permissions []string
	switch tier {
	case edge.TierBootstrap:
		permissions = bootstrapPermissions()
	case edge.TierDeploy:
		permissions = deployPermissions()
	default:
		return edge.CredentialDocument{}, fmt.Errorf(
			"cloudflare: the token permissions are listed for the bootstrap tier or the deploy tier, not %q", string(tier))
	}
	return edge.CredentialDocument{
		Heading:  credentialHeading,
		Document: strings.Join(permissions, "\n"),
	}, nil
}

func (p *cloudflare) verifyCredentials(ctx context.Context) (edge.CredentialIdentity, error) {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return edge.CredentialIdentity{}, fmt.Errorf("%s is not set", envAccountID)
	}
	if os.Getenv(envAPIToken) == "" {
		return edge.CredentialIdentity{}, fmt.Errorf("%s is not set", envAPIToken)
	}
	if _, err := p.client.Accounts.Get(ctx, accounts.AccountGetParams{AccountID: cf.F(accountID)}); err != nil {
		return edge.CredentialIdentity{}, fmt.Errorf("%s was rejected by Cloudflare for account %s: %w", envAPIToken, accountID, err)
	}
	return edge.CredentialIdentity{Account: accountID}, nil
}
