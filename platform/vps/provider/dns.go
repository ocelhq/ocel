package vps

import (
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type dns struct{}

const dnsCloudflare = providerkit.DNSKind(cloudflare.Kind)

func (dns) Open(kind providerkit.DNSKind, zone string, _ edge.Kind) (edge.DNSRecords, error) {
	if kind != dnsCloudflare {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"dns %q is not supported; use %s", kind, dnsCloudflare)
	}
	writer, err := cloudflare.NewDNS(zone)
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeInvalid, "%s", err)
	}
	return writer, nil
}
