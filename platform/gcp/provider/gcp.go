package gcp

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const Vendor providerkit.Vendor = "gcp"

const edgeNamespace = "ocel"

type Options struct {
	Project string `json:"project"`
	Region  string `json:"region"`
}

type Provider struct {
	options  Options
	tokens   TokenSource
	endpoint string
	clients  *clients
}

func New(_ context.Context, options providerkit.Options) (providerkit.Provider, error) {
	decoded, err := providerkit.Decode[Options](options)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(decoded.Project) == "" {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"option %q names no Google Cloud project, and every name this provider reads or writes is scoped to one", "project")
	}
	if strings.TrimSpace(decoded.Region) == "" {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"option %q names no region, and a project spans them all: name the one this deploy runs in", "region")
	}
	return NewProvider(decoded), nil
}

func NewProvider(options Options) *Provider {
	endpoint := emulatorEndpoint()
	return &Provider{
		options:  options,
		tokens:   ApplicationDefault{},
		endpoint: endpoint,
		clients:  &clients{project: options.Project, region: options.Region, endpoint: endpoint},
	}
}

func (p *Provider) Vendor() providerkit.Vendor { return Vendor }

func (p *Provider) Serves() []providerkit.LinkType { return resources.Serves(p) }

func (p *Provider) Computes() []providerkit.Compute {
	return []providerkit.Compute{providerkit.ComputeServerless, providerkit.ComputeContainer}
}

func (p *Provider) Bootstrap(edge.Kind) (providerkit.Bootstrapper, error) {
	return bootstrapper{}, nil
}

func (p *Provider) Releases() providerkit.Releaser { return releaser{} }

func (p *Provider) Artifacts() providerkit.ArtifactStore { return artifacts{clients: p.clients} }

func (p *Provider) Records() providerkit.RecordStore { return records{clients: p.clients} }

func (p *Provider) Sealer() providerkit.Sealer { return sealer{clients: p.clients} }

func (p *Provider) Credentials() providerkit.Credentials {
	return Credentials{
		Project:  p.options.Project,
		Region:   p.options.Region,
		Tokens:   p.tokens,
		Endpoint: p.endpoint,
		Projects: resourceManager{endpoint: p.endpoint},
	}
}

func (p *Provider) Edges() providerkit.EdgeRegistry { return edges{} }

func (p *Provider) DNS() providerkit.DNSRegistry { return dns{} }

var _ providerkit.Provider = (*Provider)(nil)
