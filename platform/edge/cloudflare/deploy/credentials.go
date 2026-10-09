package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/accounts"
	"github.com/cloudflare/cloudflare-go/v4/shared"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
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
	"Zone · Zone Settings · Read",
	"Zone · DNS · Edit",
	"Zone · SSL and Certificates · Edit",
	"Zone · Workers Routes · Edit",
}

func bootstrapPermissions() []string {
	return slices.Concat(accountPermissions, tokenMinting, zonePermissions)
}

func deployPermissions() []string {
	return slices.Concat(accountPermissions, zonePermissions)
}

var proxyPermissions = []string{
	"Account · Account Settings · Read",
	"Account · Cloudflare Tunnel · Edit (only when the edge sets `tunnel`)",
	"Zone · Zone · Read",
	"Zone · Zone Settings · Read",
	"Zone · DNS · Edit",
	"Zone · SSL and Certificates · Edit",
	"Zone · Cache Purge · Purge",
}

func proxyCredentialPermissions(purpose edge.CredentialPurpose) (edge.CredentialDocument, error) {
	switch purpose {
	case edge.PurposeBootstrap, edge.PurposeDeploy:
		return edge.CredentialDocument{Heading: credentialHeading, Document: strings.Join(proxyPermissions, "\n")}, nil
	}
	return edge.CredentialDocument{}, fmt.Errorf(
		"cloudflare: the token permissions are listed for bootstrap or deploy credentials, not %q", string(purpose))
}

func (p *cloudflare) describeCredentialPermissions(purpose edge.CredentialPurpose) (edge.CredentialDocument, error) {
	var permissions []string
	switch purpose {
	case edge.PurposeBootstrap:
		permissions = bootstrapPermissions()
		if p.workerClientCertificate {
			permissions = slices.Concat(permissions, workerClientCertificatePermissions)
		}
	case edge.PurposeDeploy:
		permissions = deployPermissions()
	default:
		return edge.CredentialDocument{}, fmt.Errorf(
			"cloudflare: the token permissions are listed for bootstrap or deploy credentials, not %q", string(purpose))
	}
	return edge.CredentialDocument{
		Heading:  credentialHeading,
		Document: strings.Join(permissions, "\n"),
	}, nil
}

func (p *cloudflare) verifyCredentials(ctx context.Context) (edge.CredentialIdentity, error) {
	accountID := readAccountID()
	if accountID == "" {
		return edge.CredentialIdentity{}, fmt.Errorf("%s is not set", envAccountID)
	}
	if os.Getenv(envAPIToken) == "" {
		return edge.CredentialIdentity{}, fmt.Errorf("%s is not set", envAPIToken)
	}
	if p.skipChecks {
		return edge.CredentialIdentity{Account: accountID}, nil
	}
	if _, err := p.client.Accounts.Get(ctx, accounts.AccountGetParams{AccountID: cf.F(accountID)}); err != nil {
		return edge.CredentialIdentity{}, refuseUnverifiedToken(accountID, err)
	}
	return edge.CredentialIdentity{Account: accountID}, nil
}

var invalidTokenCodes = []int64{1000, 6003, 6111, 9109}

func refuseUnverifiedToken(accountID string, err error) error {
	var answered *cf.Error
	switch {
	case isRateLimited(err):
		return refusal.Refuse(refusal.CodeBusy, "%s could not be checked for account %s: %v", envAPIToken, accountID, err)
	case errors.As(err, &answered) && isTokenRejection(answered):
		return refusal.Refuse(refusal.CodeDenied, "%s was rejected by Cloudflare for account %s: %v", envAPIToken, accountID, err)
	}
	return refusal.Refuse(refusal.CodeNotReady, "%s could not be checked for account %s: %v", envAPIToken, accountID, err)
}

func isTokenRejection(answered *cf.Error) bool {
	if answered.StatusCode == http.StatusUnauthorized || answered.StatusCode == http.StatusForbidden {
		return true
	}
	return slices.ContainsFunc(answered.Errors, func(e shared.ErrorData) bool { return slices.Contains(invalidTokenCodes, e.Code) })
}
