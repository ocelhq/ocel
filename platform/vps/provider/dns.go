package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

//go:generate go generate -C ../../edge/cloudflare/deploy ./...

type dns struct {
	cloudflareOrigin func(context.Context) (cloudflare.OriginScope, error)
}

const dnsCloudflare = provider.DNSKind(cloudflare.Kind)

func (d dns) Open(kind provider.DNSKind, zone string, _ edge.Kind) (edge.DNSRecords, error) {
	if kind != dnsCloudflare {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"dns %q is not supported; use %s", kind, dnsCloudflare)
	}
	return cloudflare.NewDNS(zone, d.cloudflareOrigin), nil
}
