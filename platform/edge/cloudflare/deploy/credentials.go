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
	"Account · Account Settings · Read",
	"Account · Billing · Read",
}

const zoneCertificatesPermission = "Zone · SSL and Certificates · Edit"

var zonePermissions = []string{
	"Zone · Zone · Read",
	"Zone · Zone Settings · Read",
	"Zone · DNS · Edit",
	zoneCertificatesPermission,
	"Zone · Workers Routes · Edit",
}

func workerPermissions() []string {
	return slices.Concat(accountPermissions, zonePermissions)
}

var proxyPermissions = []string{
	"Account · Account Settings · Read",
	"Account · Cloudflare Tunnel · Edit (only when the edge sets `tunnel`)",
	"Zone · Zone · Read",
	"Zone · Zone Settings · Read",
	"Zone · DNS · Edit",
	zoneCertificatesPermission,
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
	case edge.PurposeBootstrap, edge.PurposeDeploy:
		permissions = workerPermissions()
	default:
		return edge.CredentialDocument{}, fmt.Errorf(
			"cloudflare: the token permissions are listed for bootstrap or deploy credentials, not %q", string(purpose))
	}
	return edge.CredentialDocument{
		Heading:  credentialHeading,
		Document: strings.Join(permissions, "\n"),
	}, nil
}

const (
	apiTokensPage = "https://dash.cloudflare.com/profile/api-tokens"
	setupDocs     = "https://ocel.dev/docs/providers/cloudflare#setup"
)

func refuseUnsetCredentials(accountID string) error {
	var unset []string
	if accountID == "" {
		unset = append(unset, envAccountID)
	}
	if os.Getenv(envAPIToken) == "" {
		unset = append(unset, envAPIToken)
	}
	var headline string
	switch len(unset) {
	case 0:
		return nil
	case 1:
		headline = unset[0] + " is not set"
	default:
		headline = strings.Join(unset, " and ") + " are not set"
	}
	return refusal.Refuse(refusal.CodeDenied,
		"%s: set %s to your Cloudflare account's ID and %s to an API token created at %s with the permissions %s lists, in the environment ocel runs in, such as by exporting them in your shell",
		headline, envAccountID, envAPIToken, apiTokensPage, setupDocs)
}

func (p *cloudflare) verifyCredentials(ctx context.Context) (edge.CredentialIdentity, error) {
	accountID := readAccountID()
	if err := refuseUnsetCredentials(accountID); err != nil {
		return edge.CredentialIdentity{}, err
	}
	if p.skipChecks {
		return edge.CredentialIdentity{Account: accountID}, nil
	}
	if _, err := p.client.Accounts.Get(ctx, accounts.AccountGetParams{AccountID: cf.F(accountID)}); err != nil {
		return edge.CredentialIdentity{}, refuseUnverifiedToken(ctx, accountID, err)
	}
	return edge.CredentialIdentity{Account: accountID}, nil
}

var invalidTokenCodes = []int64{1000, 6003, 6111, 9109}

const authenticationErrorCode = 10000

func refuseZoneCertificatesFailure(zoneName, doing string, err error) error {
	var answered *cf.Error
	denied := errors.As(err, &answered) && (answered.StatusCode == http.StatusForbidden ||
		slices.ContainsFunc(answered.Errors, func(e shared.ErrorData) bool { return e.Code == authenticationErrorCode }))
	if !denied {
		return fmt.Errorf("%s: %w", doing, err)
	}
	return refusal.Refuse(refusal.CodeDenied,
		"%s may not %s: give it the permission %s on zone %s at %s, as %s lists, and deploy again (Cloudflare answered: %v)",
		envAPIToken, doing, zoneCertificatesPermission, zoneName, apiTokensPage, setupDocs, err)
}

func refuseUnverifiedToken(ctx context.Context, accountID string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("check %s for account %s: %w", envAPIToken, accountID, err)
	}
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
