package gcp

import (
	"github.com/ocelhq/ocel/pkg/providerkit"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type dns struct{}

const dnsCloudflare = providerkit.DNSKind(cloudflare.Kind)

func (dns) Open(kind providerkit.DNSKind, zone string, _ edge.Kind) (edge.DNSRecords, error) {
	if kind != dnsCloudflare {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"this provider cannot write DNS records with %q; it writes them with %s", kind, dnsCloudflare)
	}
	writer, err := cloudflare.NewDNS(zone)
	if err != nil {
		return nil, providerkit.Refuse(providerkit.CodeInvalid, "%s", err)
	}
	return writer, nil
}
