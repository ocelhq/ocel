package vps

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

type credentials struct{ provider *Provider }

type hostSurvey interface {
	Facts(ctx context.Context) (session.Facts, error)
	HostKey() provider.HostKey
	Destination() session.Destination
}

func (c credentials) Whoami(ctx context.Context) (provider.Principal, error) {
	live, err := c.provider.Session(ctx)
	if err != nil {
		return provider.Principal{}, err
	}
	return whoami(ctx, live)
}

func whoami(ctx context.Context, live hostSurvey) (provider.Principal, error) {
	facts, err := live.Facts(ctx)
	if err != nil {
		return provider.Principal{}, err
	}
	dest := live.Destination()
	key := live.HostKey()
	return provider.Principal{
		Vendor:  Vendor,
		Account: dest.Written,
		Name:    dest.User,
		Details: named([]provider.PrincipalDetail{
			{Label: "host key", Value: strings.TrimSpace(key.Type + " " + key.Fingerprint)},
			{Label: "address", Value: fmt.Sprintf("%s port %d", dest.Address, dest.Port)},
			{Label: "os", Value: facts.OS},
			{Label: "arch", Value: facts.Arch},
			{Label: "elevation", Value: elevation(facts)},
		}),
	}, nil
}

func elevation(facts session.Facts) string {
	switch {
	case facts.Root:
		return "root"
	case facts.Sudo:
		return "sudo without a password"
	default:
		return "neither root nor sudo without a password"
	}
}

func named(details []provider.PrincipalDetail) []provider.PrincipalDetail {
	var out []provider.PrincipalDetail
	for _, detail := range details {
		if detail.Value != "" {
			out = append(out, detail)
		}
	}
	return out
}

func (c credentials) Permissions(tier edge.CredentialTier) (edge.CredentialDocument, error) {
	switch tier {
	case edge.TierBootstrap:
		return edge.CredentialDocument{Document: bootstrapDocument(c.login())}, nil
	case edge.TierDeploy:
		return edge.CredentialDocument{Document: deployDocument()}, nil
	default:
		return edge.CredentialDocument{}, refusal.Refuse(refusal.CodeInvalid,
			"unknown credential tier: want bootstrap or deploy")
	}
}

func (c credentials) login() string {
	if user := strings.TrimSpace(c.provider.options.SSH.User); user != "" {
		return user
	}
	return anyLogin
}
