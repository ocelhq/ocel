package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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

type credentials struct{}

func (credentials) Whoami(context.Context) (providerkit.Identity, error) {
	return providerkit.Identity{Provider: Vendor}, nil
}

func (credentials) Permissions(providerkit.CredentialTier) (string, error) { return "", nil }

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
