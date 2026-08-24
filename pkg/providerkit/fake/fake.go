package fake

import (
	"context"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const Vendor providerkit.Vendor = "fake"

type Options struct {
	Region string `json:"region,omitempty"`
}

type Provider struct {
	mu          sync.Mutex
	pins        map[string]string
	certRefusal error

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

func (p *Provider) Bootstrap(kind edge.Kind) (providerkit.Bootstrapper, error) {
	if _, err := p.edges.Open(kind); err != nil {
		return nil, err
	}
	p.bootstrap.fronting(kind)
	return p.bootstrap, nil
}

func (p *Provider) Bootstrapper() *Bootstrapper { return p.bootstrap }

func (p *Provider) Releases() providerkit.Releaser { return p.releases }

func (p *Provider) Artifacts() providerkit.ArtifactStore { return p.artifacts }

func (p *Provider) Records() providerkit.RecordStore { return p.records }

func (p *Provider) Sealer() providerkit.Sealer { return p.sealer }

func (p *Provider) Credentials() providerkit.Credentials { return p.creds }

func (p *Provider) Edges() providerkit.EdgeRegistry { return p.edges }

func (p *Provider) DNS() providerkit.DNSRegistry { return p.dns }

func (p *Provider) Pin(hostname, certificate string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pins == nil {
		p.pins = map[string]string{}
	}
	p.pins[hostname] = certificate
}

func (p *Provider) RefuseCertificates(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.certRefusal = err
}

func (p *Provider) Certificate(_ context.Context, _ edge.Kind, hostname string, _ providerkit.Reporter) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.certRefusal != nil {
		return "", p.certRefusal
	}
	return p.pins[hostname], nil
}

type Warmer struct{ *Provider }

func (Warmer) Warm(context.Context, []string, providerkit.Reporter) error { return nil }

type CodeEmbedder struct{ *Provider }

func (CodeEmbedder) EmbedCode(context.Context, string, providerkit.ArtifactRef, providerkit.Reporter) error {
	return nil
}

type MembraneSource struct{ *Provider }

func (MembraneSource) Membrane(context.Context) ([]byte, error) { return []byte(Membrane), nil }

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

func (Full) Membrane(context.Context) ([]byte, error) { return []byte(Membrane), nil }

var (
	_ providerkit.Provider       = (*Provider)(nil)
	_ providerkit.Warmer         = Warmer{}
	_ providerkit.CodeEmbedder   = CodeEmbedder{}
	_ providerkit.StackInspector = StackInspector{}
	_ providerkit.GrantVerifier  = GrantVerifier{}
	_ providerkit.MembraneSource = MembraneSource{}
	_ providerkit.Certifier      = (*Provider)(nil)
)

const Membrane = "fake-membrane"
