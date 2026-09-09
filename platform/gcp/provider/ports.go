package gcp

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/direct"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

func classless(what any) error {
	return providerkit.Refuse(providerkit.CodeInvalid,
		"%s names no class, and this project keeps each class's state apart from the other class's", what)
}

type edges struct {
	namespace providerkit.Namespace
	records   providerkit.RecordStore
	pins      pin.Pins
}

var supportedEdges = []edge.Kind{direct.Kind, cloudflare.Kind}

func (edges) Supported() []edge.Kind { return slices.Clone(supportedEdges) }

func (edges) Default() edge.Kind { return direct.Kind }

func (e edges) Open(kind edge.Kind) (edge.Edge, error) {
	switch kind {
	case direct.Kind:
		return direct.New(e.records, e.pins), nil
	case cloudflare.Kind:
		return cloudflare.New(string(e.namespace)), nil
	}
	return nil, providerkit.Refuse(providerkit.CodeInvalid,
		"this provider cannot front deployments with the %q edge; it fronts them with %s and %s",
		kind, direct.Kind, cloudflare.Kind)
}

type dns struct{}

const dnsCloudflare = providerkit.DNSKind(cloudflare.Kind)

func (dns) Supported() []providerkit.DNSKind { return []providerkit.DNSKind{dnsCloudflare} }

func (dns) Default() providerkit.DNSKind { return "" }

func (dns) Open(kind providerkit.DNSKind, zone string) (edge.DNSWriter, error) {
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

var (
	_ providerkit.Bootstrapper  = bootstrapper{}
	_ providerkit.ArtifactStore = artifacts{}
	_ providerkit.RecordStore   = records{}
	_ providerkit.Sealer        = sealer{}
	_ providerkit.EdgeRegistry  = edges{}
	_ providerkit.DNSRegistry   = dns{}
)
