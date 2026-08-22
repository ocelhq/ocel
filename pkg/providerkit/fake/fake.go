package fake

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const Vendor providerkit.Vendor = "fake"

type Options struct {
	Region string `json:"region,omitempty"`
}

type Provider struct {
	options   Options
	records   *Records
	artifacts *Artifacts
	sealer    *Sealer
	bootstrap *Bootstrapper
	releases  *Releaser
	creds     *Credentials
	edges     *Edges
	dns       *DNS
}

func New(_ context.Context, options providerkit.Options) (providerkit.Provider, error) {
	decoded, err := providerkit.Decode[Options](options)
	if err != nil {
		return nil, err
	}
	return Full{NewProvider(decoded)}, nil
}

func NewProvider(options Options) *Provider {
	records := NewRecords()
	return &Provider{
		options:   options,
		records:   records,
		artifacts: NewArtifacts(),
		sealer:    NewSealer(),
		bootstrap: NewBootstrapper(),
		releases:  NewReleaser(),
		creds:     NewCredentials(options.Region),
		edges:     NewEdges(records),
		dns:       NewDNS(),
	}
}

func (p *Provider) Vendor() providerkit.Vendor { return Vendor }

func (p *Provider) Serves() []providerkit.LinkType {
	return []providerkit.LinkType{providerkit.LinkPostgres, providerkit.LinkBucket}
}

func (p *Provider) Region() string { return p.options.Region }

func (p *Provider) Bootstrap() providerkit.Bootstrapper { return p.bootstrap }

func (p *Provider) Releases() providerkit.Releaser { return p.releases }

func (p *Provider) Artifacts() providerkit.ArtifactStore { return p.artifacts }

func (p *Provider) Records() providerkit.RecordStore { return p.records }

func (p *Provider) Sealer() providerkit.Sealer { return p.sealer }

func (p *Provider) Credentials() providerkit.Credentials { return p.creds }

func (p *Provider) Edges() providerkit.EdgeRegistry { return p.edges }

func (p *Provider) DNS() providerkit.DNSRegistry { return p.dns }

type Warmer struct{ *Provider }

func (Warmer) Warm(context.Context, []string, providerkit.Reporter) error { return nil }

type CodeEmbedder struct{ *Provider }

func (CodeEmbedder) EmbedCode(context.Context, string, providerkit.ArtifactRef, providerkit.Reporter) error {
	return nil
}

type StackInspector struct{ *Provider }

func (s StackInspector) Inspect(_ context.Context, ref providerkit.StackRef) (providerkit.StackState, error) {
	return s.releases.State(ref), nil
}

type GrantVerifier struct{ *Provider }

func (GrantVerifier) VerifyGrants(context.Context, providerkit.Link) error { return nil }

type Full struct{ *Provider }

func (Full) Warm(context.Context, []string, providerkit.Reporter) error { return nil }

func (Full) EmbedCode(context.Context, string, providerkit.ArtifactRef, providerkit.Reporter) error {
	return nil
}

func (f Full) Inspect(_ context.Context, ref providerkit.StackRef) (providerkit.StackState, error) {
	return f.releases.State(ref), nil
}

func (Full) VerifyGrants(context.Context, providerkit.Link) error { return nil }

var (
	_ providerkit.Provider       = (*Provider)(nil)
	_ providerkit.Warmer         = Warmer{}
	_ providerkit.CodeEmbedder   = CodeEmbedder{}
	_ providerkit.StackInspector = StackInspector{}
	_ providerkit.GrantVerifier  = GrantVerifier{}
)
