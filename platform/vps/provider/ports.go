package vps

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

type releaser struct{}

func (releaser) Plan(context.Context, providerkit.StackPlan, providerkit.Reporter) (providerkit.Plan, error) {
	return providerkit.Plan{}, nil
}

func (releaser) Provision(context.Context, providerkit.StackPlan, providerkit.Reporter) (providerkit.StackResult, error) {
	return providerkit.StackResult{}, nil
}

func (releaser) PlanDestroy(context.Context, providerkit.StackRef, providerkit.Reporter) (providerkit.Plan, error) {
	return providerkit.Plan{}, nil
}

func (releaser) Destroy(context.Context, providerkit.StackRef, providerkit.Reporter) error {
	return nil
}

type credentials struct{ provider *Provider }

func (c credentials) Whoami(ctx context.Context) (providerkit.Identity, error) {
	live, err := c.provider.Session(ctx)
	if err != nil {
		return providerkit.Identity{}, err
	}
	facts, err := live.Preflight(ctx)
	if err != nil {
		return providerkit.Identity{}, err
	}
	dest := live.Destination()
	return providerkit.Identity{
		Provider:  Vendor,
		Account:   live.HostKey().Fingerprint,
		Principal: dest.Principal(),
		Details: named([]providerkit.Detail{
			{Label: "address", Value: fmt.Sprintf("%s port %d", dest.Address, dest.Port)},
			{Label: "host key", Value: live.HostKey().Type},
			{Label: "os", Value: facts.OS},
			{Label: "arch", Value: facts.Arch},
			{Label: "elevation", Value: elevation(facts)},
		}),
	}, nil
}

func elevation(facts session.Facts) string {
	if facts.Root {
		return "root"
	}
	return "sudo without a password"
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
		return edge.CredentialDocument{}, providerkit.Refuse(providerkit.CodeInvalid,
			"a host carries the bootstrap credentials or the deploy credentials; this request named neither")
	}
}

func (c credentials) login() string {
	if user := strings.TrimSpace(c.provider.options.SSH.User); user != "" {
		return user
	}
	return anyLogin
}

type edges struct{}

func (edges) Supported() []edge.Kind { return nil }

func (edges) Default() edge.Kind { return "" }

func (edges) Open(kind edge.Kind) (edge.Edge, error) {
	return nil, providerkit.Refuse(providerkit.CodeInvalid, "the vps provider serves no edge %q", kind)
}

type dns struct{}

func (dns) Supported() []providerkit.DNSKind { return nil }

func (dns) Default() providerkit.DNSKind { return "" }

func (dns) Open(kind providerkit.DNSKind, _ string) (edge.DNSWriter, error) {
	return nil, providerkit.Refuse(providerkit.CodeInvalid, "the vps provider writes no dns %q", kind)
}

var (
	_ providerkit.Releaser     = releaser{}
	_ providerkit.Credentials  = credentials{}
	_ providerkit.EdgeRegistry = edges{}
	_ providerkit.DNSRegistry  = dns{}
)
