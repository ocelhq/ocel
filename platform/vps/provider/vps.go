package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

const Vendor providerkit.Vendor = "vps"

type Options struct {
	Host string `json:"host,omitempty"`
}

type Provider struct {
	options   Options
	records   *fake.Records
	artifacts *fake.Artifacts
	sealer    *fake.Sealer
}

func New(_ context.Context, options providerkit.Options) (providerkit.Provider, error) {
	decoded, err := providerkit.Decode[Options](options)
	if err != nil {
		return nil, err
	}
	return NewProvider(decoded), nil
}

func NewProvider(options Options) *Provider {
	return &Provider{
		options:   options,
		records:   fake.NewRecords(),
		artifacts: fake.NewArtifacts(),
		sealer:    fake.NewSealer(),
	}
}

func (p *Provider) Vendor() providerkit.Vendor { return Vendor }

func (p *Provider) Host() string { return p.options.Host }

func (p *Provider) Serves() []providerkit.LinkType { return nil }

func (p *Provider) Bootstrap() providerkit.Bootstrapper { return bootstrapper{} }

func (p *Provider) Releases() providerkit.Releaser { return releaser{} }

func (p *Provider) Artifacts() providerkit.ArtifactStore { return p.artifacts }

func (p *Provider) Records() providerkit.RecordStore { return p.records }

func (p *Provider) Sealer() providerkit.Sealer { return p.sealer }

func (p *Provider) Credentials() providerkit.Credentials { return credentials{} }

func (p *Provider) Edges() providerkit.EdgeRegistry { return edges{} }

func (p *Provider) DNS() providerkit.DNSRegistry { return dns{} }

var _ providerkit.Provider = (*Provider)(nil)
