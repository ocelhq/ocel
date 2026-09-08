package gcp

import (
	"context"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const Vendor providerkit.Vendor = "gcp"

type Options struct {
	Project string `json:"project"`
	Region  string `json:"region"`
}

type Provider struct {
	options  Options
	tokens   TokenSource
	endpoint string
	clients  *clients

	bases map[string]base
	pull  func(ctx context.Context, ref string) (v1.Image, error)
}

func New(ctx context.Context, options providerkit.Options) (providerkit.Provider, error) {
	decoded, err := providerkit.Decode[Options](options)
	if err != nil {
		return nil, err
	}
	provider, err := NewProvider(ctx, decoded)
	if err != nil {
		return nil, err
	}
	return provider, nil
}

func NewProvider(ctx context.Context, options Options) (*Provider, error) {
	if strings.TrimSpace(options.Region) == "" {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"option %q names no region, and a project spans them all: name the one this deploy runs in", "region")
	}
	endpoint, err := emulatorEndpoint()
	if err != nil {
		return nil, err
	}
	namespace, err := providerkit.NamespaceFromEnv()
	if err != nil {
		return nil, err
	}
	project, err := resolveProject(ctx, options.Project)
	if err != nil {
		return nil, err
	}
	names := Names{namespace: namespace, project: project}
	if err := names.fit(); err != nil {
		return nil, err
	}
	options.Project = project
	return &Provider{
		options:  options,
		tokens:   ApplicationDefault{},
		endpoint: endpoint,
		clients:  &clients{Names: names, region: options.Region, endpoint: endpoint},
		bases:    functionBases(),
		pull:     pullBase,
	}, nil
}

func (p *Provider) Names() Names { return p.clients.Names }

func (p *Provider) Vendor() providerkit.Vendor { return Vendor }

func (p *Provider) Serves() []providerkit.LinkType { return resources.Serves(p) }

func (p *Provider) Computes() []providerkit.Compute {
	return []providerkit.Compute{providerkit.ComputeServerless, providerkit.ComputeContainer}
}

func (p *Provider) Bootstrap(edge.Kind) (providerkit.Bootstrapper, error) {
	return bootstrapper{clients: p.clients}, nil
}

func (p *Provider) Releases() providerkit.Releaser {
	return resources.Releaser(p.Records(), p.Artifacts(), p)
}

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

func (p *Provider) Edges() providerkit.EdgeRegistry {
	return edges{namespace: p.clients.Namespace()}
}

func (p *Provider) DNS() providerkit.DNSRegistry { return dns{} }

var _ providerkit.Provider = (*Provider)(nil)
