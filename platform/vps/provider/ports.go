package vps

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

type bootstrapper struct{}

func (bootstrapper) Catalogue() []providerkit.Feature { return nil }

func (bootstrapper) Describe(_ context.Context, class providerkit.Class) (providerkit.Bootstrap, error) {
	return providerkit.Bootstrap{Class: class}, nil
}

func (b bootstrapper) Plan(ctx context.Context, req providerkit.BootstrapRequest) (providerkit.BootstrapPlan, error) {
	described, err := b.Describe(ctx, req.Class)
	if err != nil {
		return providerkit.BootstrapPlan{}, err
	}
	return providerkit.BootstrapPlan{Groups: providerkit.DeriveGroups(described, b.Catalogue(), req)}, nil
}

func (bootstrapper) Apply(context.Context, providerkit.BootstrapRequest, providerkit.Reporter) error {
	return nil
}

func (bootstrapper) PlanRemoval(context.Context, providerkit.Class) (providerkit.BootstrapPlan, error) {
	return providerkit.BootstrapPlan{}, nil
}

func (bootstrapper) Remove(context.Context, providerkit.Class, providerkit.Reporter) error {
	return nil
}

type releaser struct{}

func (releaser) Provision(context.Context, providerkit.StackPlan, providerkit.Reporter) (providerkit.StackResult, error) {
	return providerkit.StackResult{}, nil
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
		Account:   live.Fingerprint(),
		Principal: dest.Principal(),
		Details: details(
			"address", fmt.Sprintf("%s port %d", dest.Address, dest.Port),
			"host key", live.HostKey().Type,
			"os", facts.OS,
			"arch", facts.Arch,
			"elevation", elevation(facts),
		),
	}, nil
}

func elevation(facts session.Facts) string {
	if facts.Root {
		return "root"
	}
	return "sudo without a password"
}

func details(pairs ...string) []providerkit.Detail {
	var out []providerkit.Detail
	for at := 0; at+1 < len(pairs); at += 2 {
		if pairs[at+1] == "" {
			continue
		}
		out = append(out, providerkit.Detail{Label: pairs[at], Value: pairs[at+1]})
	}
	return out
}

func (credentials) Permissions(providerkit.CredentialTier) (edge.CredentialDocument, error) {
	return edge.CredentialDocument{Heading: "VPS credentials"}, nil
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
	_ providerkit.Bootstrapper = bootstrapper{}
	_ providerkit.Releaser     = releaser{}
	_ providerkit.Credentials  = credentials{}
	_ providerkit.EdgeRegistry = edges{}
	_ providerkit.DNSRegistry  = dns{}
)
