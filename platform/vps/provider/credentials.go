package vps

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

type credentials struct{ provider *Provider }

type hostSurvey interface {
	Facts(ctx context.Context) (session.Facts, error)
	HostKey() providerkit.HostKey
	Destination() session.Destination
}

func (c credentials) Whoami(ctx context.Context) (providerkit.Identity, error) {
	live, err := c.provider.Session(ctx)
	if err != nil {
		return providerkit.Identity{}, err
	}
	return whoami(ctx, live)
}

func whoami(ctx context.Context, live hostSurvey) (providerkit.Identity, error) {
	facts, err := live.Facts(ctx)
	if err != nil {
		return providerkit.Identity{}, err
	}
	dest := live.Destination()
	key := live.HostKey()
	return providerkit.Identity{
		Vendor:    Vendor,
		Account:   dest.Written,
		Principal: dest.User,
		Details: named([]providerkit.Detail{
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

func named(details []providerkit.Detail) []providerkit.Detail {
	var out []providerkit.Detail
	for _, detail := range details {
		if detail.Value != "" {
			out = append(out, detail)
		}
	}
	return out
}

func (c credentials) Permissions(tier providerkit.CredentialTier) (edge.CredentialDocument, error) {
	switch tier {
	case providerkit.TierBootstrap:
		return edge.CredentialDocument{Document: bootstrapDocument(c.login())}, nil
	case providerkit.TierDeploy:
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
