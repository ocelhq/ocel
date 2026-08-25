package cloudflare

import (
	"fmt"
	"slices"
	"strings"

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

var _ edge.CredentialDocumenter = (*provider)(nil)

func (p *provider) CredentialPermissions(tier edge.CredentialTier) (edge.CredentialDocument, error) {
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
