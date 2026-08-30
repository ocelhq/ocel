package fake

import (
	"context"
	"slices"
	"strconv"
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
	issue       []edge.Record
	discarded   []string
	discardHeld error
	rotation    int
	pending     error
	health      *providerkit.CertificateHealth

	preflightRefusal error
	preflighted      []providerkit.DeployPreflight

	options   Options
	records   *Records
	artifacts providerkit.ArtifactStore
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
	artifacts := NewArtifacts()
	return &Provider{
		options:   options,
		records:   records,
		artifacts: artifacts,
		sealer:    NewSealer(),
		bootstrap: NewBootstrapper(),
		releases:  NewReleaser(artifacts),
		creds:     NewCredentials(options.Region),
		edges:     NewEdges(records),
		dns:       NewDNS(),
	}
}

func (p *Provider) Ships(store providerkit.ArtifactStore) *Provider {
	p.artifacts = store
	p.releases.artifacts = store
	return p
}

func (p *Provider) Vendor() providerkit.Vendor { return Vendor }

func (p *Provider) Serves() []providerkit.LinkType {
	return []providerkit.LinkType{providerkit.LinkPostgres, providerkit.LinkBucket}
}

func (p *Provider) Computes() []providerkit.Compute {
	return []providerkit.Compute{providerkit.ComputeServerless, providerkit.ComputeContainer}
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

func (p *Provider) IssueCertificates(validation ...edge.Record) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.issue = validation
}

func (p *Provider) RotateCertificates() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rotation++
}

func (p *Provider) StallAfterProving(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending = err
}

func (p *Provider) RefuseDiscardingAServingCertificate(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.discardHeld = err
}

func (p *Provider) Discarded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.discarded)
}

func (p *Provider) Certificate(ctx context.Context, req providerkit.CertificateRequest) (providerkit.Certificate, error) {
	p.mu.Lock()
	refusal, pinned, validation := p.certRefusal, p.pins[req.Hostname], slices.Clone(p.issue)
	rotation, pending := p.rotation, p.pending
	p.mu.Unlock()

	if refusal != nil {
		return providerkit.Certificate{}, refusal
	}
	if pinned != "" {
		return providerkit.Certificate{ID: pinned}, nil
	}
	if validation == nil {
		return providerkit.Certificate{}, nil
	}
	cert := providerkit.Certificate{ID: issuedFor(req.Hostname, rotation), Requested: true}
	if req.Held.Requested && req.Held.ID == cert.ID {
		return req.Held, nil
	}
	settled, err := req.Prove(ctx, cert, validation)
	if err != nil {
		return settled, err
	}
	return settled, pending
}

func issuedFor(hostname string, rotation int) string {
	id := "issued-for-" + hostname
	if rotation == 0 {
		return id
	}
	return id + "-" + strconv.Itoa(rotation)
}

func (p *Provider) ReportCertificate(health providerkit.CertificateHealth) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.health = &health
}

func (p *Provider) InspectCertificate(_ context.Context, _ edge.Kind, hostname string, cert providerkit.Certificate) (providerkit.CertificateHealth, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.health != nil {
		return *p.health, nil
	}
	if p.pins == nil && p.issue == nil {
		return providerkit.CertificateHealth{}, nil
	}
	health := providerkit.CertificateHealth{Terminates: true}
	if cert.ID == "" {
		return health, nil
	}
	health.Status, health.Issued = "issued", true
	health.Domains, health.Covers = []string{hostname}, true
	return health, nil
}

func (p *Provider) DiscardCertificate(_ context.Context, cert providerkit.Certificate, _ providerkit.Reporter) error {
	p.mu.Lock()
	refusal := p.discardHeld
	p.mu.Unlock()
	if refusal != nil && p.edges.serving(cert.ID) {
		return refusal
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.discarded = append(p.discarded, cert.ID)
	return nil
}

func (p *Provider) RefusePreflight(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.preflightRefusal = err
}

func (p *Provider) Preflighted() []providerkit.DeployPreflight {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.preflighted)
}

func (p *Provider) preflight(pre providerkit.DeployPreflight) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.preflighted = append(p.preflighted, pre)
	return p.preflightRefusal
}

type DeployPreflighter struct{ *Provider }

func (d DeployPreflighter) PreflightDeploy(_ context.Context, pre providerkit.DeployPreflight) error {
	return d.preflight(pre)
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

func (f Full) PreflightDeploy(_ context.Context, pre providerkit.DeployPreflight) error {
	return f.preflight(pre)
}

var (
	_ providerkit.Provider          = (*Provider)(nil)
	_ providerkit.Warmer            = Warmer{}
	_ providerkit.CodeEmbedder      = CodeEmbedder{}
	_ providerkit.StackInspector    = StackInspector{}
	_ providerkit.GrantVerifier     = GrantVerifier{}
	_ providerkit.MembraneSource    = MembraneSource{}
	_ providerkit.DeployPreflighter = DeployPreflighter{}
	_ providerkit.Certifier         = (*Provider)(nil)
	_ providerkit.EdgeProgrammer    = (*Provider)(nil)
)

const Membrane = "fake-membrane"
