package gcp

import (
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type dns struct{}

const dnsCloudflare = provider.DNSKind(cloudflare.Kind)

func (dns) Open(kind provider.DNSKind, zone string, _ edge.Kind) (edge.DNSRecords, error) {
	if kind != dnsCloudflare {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"this provider cannot write DNS records with %q; it writes them with %s", kind, dnsCloudflare)
	}
	writer, err := cloudflare.NewDNS(zone)
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeInvalid, "%s", err)
	}
	return writer, nil
}
