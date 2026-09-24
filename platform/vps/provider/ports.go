package vps

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

type credentials struct{ provider *Provider }

type surveyor interface {
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

func whoami(ctx context.Context, live surveyor) (providerkit.Identity, error) {
	facts, err := live.Facts(ctx)
	if err != nil {
		return providerkit.Identity{}, err
	}
	dest := live.Destination()
	key := live.HostKey()
	return providerkit.Identity{
		Provider:  Vendor,
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
		return edge.CredentialDocument{}, providerkit.Refuse(providerkit.CodeInvalid,
			"unknown credential tier: want bootstrap or deploy")
	}
}

func (c credentials) login() string {
	if user := strings.TrimSpace(c.provider.options.SSH.User); user != "" {
		return user
	}
	return anyLogin
}

type edges struct{ provider *Provider }

func (edges) Supported() []edge.Kind { return []edge.Kind{box.Kind} }

func (edges) Default() edge.Kind { return box.Kind }

func (e edges) Open(kind edge.Kind) (edge.Edge, error) {
	if kind != box.Kind {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"edge %q is not supported; use %q", kind, box.Kind)
	}
	return e.provider.box(), nil
}

type dns struct{}

const dnsCloudflare = providerkit.DNSKind(cloudflare.Kind)

func (dns) Supported() []providerkit.DNSKind {
	return []providerkit.DNSKind{dnsCloudflare}
}

func (dns) Default() providerkit.DNSKind { return "" }

func (dns) Open(kind providerkit.DNSKind, zone string, _ edge.Kind) (edge.DNSWriter, error) {
	if kind != dnsCloudflare {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"dns %q is not supported; use %s", kind, dnsCloudflare)
	}
	writer, err := cloudflare.NewDNS(zone)
	if err != nil {
		return nil, providerkit.Refuse(providerkit.CodeInvalid, "%s", err)
	}
	return writer, nil
}

var (
	_ providerkit.Credentials  = credentials{}
	_ providerkit.EdgeRegistry = edges{}
	_ providerkit.DNSRegistry  = dns{}
)
