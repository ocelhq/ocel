package fake

import (
	"context"
	"slices"
	"strconv"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
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
	wrappedFor       []string
	hooks            providerkit.Hooks

	journal   *Journal
	options   Options
	records   *Records
	artifacts providerkit.ArtifactStore
	images    *Images
	sealer    *Sealer
	bootstrap *Bootstrapper
	releases  *Releaser
	releasing providerkit.Releaser
	creds     *Credentials
	edges     *Edges
	dns       *DNS
}

func New(_ context.Context, settings providerkit.Settings) (providerkit.Provider, error) {
	decoded, err := providerkit.Decode[Options](Vendor, settings.Options)
	if err != nil {
		return nil, err
	}
	provider := NewProvider(decoded)
	return provider.Hook(provider.everyHook), nil
}

func NewProvider(options Options) *Provider {
	journal := &Journal{}
	records := NewRecords()
	records.journal = journal
	artifacts := NewArtifacts()
	artifacts.journal = journal
	p := &Provider{
		journal:   journal,
		options:   options,
		records:   records,
		artifacts: artifacts,
		images:    NewImages(),
		sealer:    NewSealer(),
		bootstrap: NewBootstrapper(),
		releases:  NewReleaser(artifacts).journalling(journal),
		creds:     NewCredentials(options.Region),
		edges:     NewEdges(records),
		dns:       NewDNS(),
	}
	p.hooks = providerkit.Hooks{
		ProgramEdge:    p.ProgramEdge,
		RegistryImages: p.RegistryImages,
		ShapeCost:      p.ShapeCost,
		EstimateCost:   p.EstimateCost,
	}
	return p
}

func (p *Provider) Hooks() providerkit.Hooks {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hooks
}

func (p *Provider) Hook(set func(*providerkit.Hooks)) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	set(&p.hooks)
	return p
}

func (p *Provider) everyHook(hooks *providerkit.Hooks) {
	hooks.WarmFunctions = func(context.Context, []string, providerkit.Reporter) error { return nil }
	hooks.EmbedCode = func(context.Context, string, providerkit.ArtifactRef, providerkit.Reporter) error { return nil }
	hooks.InspectStack = p.InspectStack
	hooks.VerifyGrants = func(context.Context, providerkit.Binding) error { return nil }
	hooks.PreflightDeploy = p.PreflightDeploy
	hooks.EnsureImageRegistry = p.EnsureImageRegistry
}

func (p *Provider) ResourceHooks() resources.Hooks {
	return resources.Hooks{
		ProvisionFunctions:  p.ProvisionFunctions,
		RemoveFunctions:     p.RemoveFunctions,
		ProvisionContainers: p.ProvisionContainers,
		RemoveContainers:    p.RemoveContainers,
	}
}

func (p *Provider) Ships(store providerkit.ArtifactStore) *Provider {
	p.artifacts = store
	p.releases.artifacts = store
	return p
}

func (p *Provider) RegistryImages(_ context.Context, target providerkit.RegistryTarget) (providerkit.ImageStore, error) {
	p.images.open(target)
	return p.images, nil
}

func (p *Provider) Registry() *Images { return p.images }

func (p *Provider) Facts() providerkit.Facts {
	return providerkit.Facts{
		Vendor:   Vendor,
		Bindings: []providerkit.BindingType{providerkit.BindingPostgres, providerkit.BindingBucket},
		Computes: []providerkit.Compute{providerkit.ComputeServerless, providerkit.ComputeContainer},
	}
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

func (p *Provider) Journal() []string { return p.journal.Entries() }

func (p *Provider) Releasing(hooks resources.Hooks) *Provider {
	p.releasing = resources.Releaser(p.records, p.artifacts, hooks)
	return p
}

func (p *Provider) Releases() providerkit.Releaser {
	if p.releasing != nil {
		return p.releasing
	}
	return p.releases
}

func (p *Provider) Releaser() *Releaser { return p.releases }

func (p *Provider) Artifacts() providerkit.ArtifactStore { return p.artifacts }

func (p *Provider) Records() providerkit.RecordStore { return p.records }

func (p *Provider) Sealer() providerkit.Sealer { return p.sealer }

func (p *Provider) Credentials() providerkit.Credentials { return p.creds }

func (p *Provider) Edges() providerkit.EdgeRegistry { return p.edges }

func (p *Provider) DNS() providerkit.DNSRegistry { return p.dns }

func (p *Provider) Serving(_ context.Context, _ edge.Kind, hostname string) (edge.Kind, error) {
	return p.edges.answering(hostname), nil
}

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

func (p *Provider) PreflightDeploy(_ context.Context, pre providerkit.DeployPreflight) error {
	return p.preflight(pre)
}

type ContainerWrapper struct {
	*Provider
	arch    string
	runtime []byte
}

func (p *Provider) WrappingContainers(arch string, runtime []byte) ContainerWrapper {
	return ContainerWrapper{Provider: p, arch: arch, runtime: runtime}
}

func (w ContainerWrapper) ContainerArch(_ context.Context, _, declared string) (string, error) {
	if declared == "" {
		return w.arch, nil
	}
	runs, _ := providerkit.GoArch(declared)
	return runs, nil
}

func (w ContainerWrapper) ContainerRuntime(_ context.Context, arch string) ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.wrappedFor = append(w.wrappedFor, arch)
	return w.runtime, nil
}

func (p *Provider) WrappedFor() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.wrappedFor)
}

func (p *Provider) InspectStack(_ context.Context, ref providerkit.StackRef) (providerkit.StackState, error) {
	return p.releases.State(ref), nil
}

func (p *Provider) EnsureImageRegistry(context.Context, providerkit.Class, []string) (providerkit.RegistryTarget, error) {
	return providerkit.RegistryTarget{Server: RegistryServer, Namespace: RegistryNamespace, Username: "fake", Password: "fake-token"}, nil
}

func (*Provider) ProvisionFunctions(_ context.Context, plan providerkit.StackPlan, _ providerkit.Reporter) ([]providerkit.Function, error) {
	return StoodUpFunctions(plan), nil
}

func (p *Provider) RemoveFunctions(_ context.Context, _ providerkit.StackRef, functions []providerkit.Function, _ providerkit.Reporter) error {
	for _, function := range functions {
		p.releases.tookDown(function.Name)
	}
	return nil
}

func (*Provider) ProvisionContainers(_ context.Context, plan providerkit.StackPlan, _ providerkit.Reporter) ([]providerkit.AppContainer, error) {
	return StoodUpContainers(plan), nil
}

func (p *Provider) RemoveContainers(_ context.Context, _ providerkit.StackRef, containers []providerkit.AppContainer, _ providerkit.Reporter) error {
	for _, container := range containers {
		p.releases.tookDown(container.Name)
	}
	return nil
}

const (
	RegistryServer    = "registry.fake.invalid"
	RegistryNamespace = "ocel"
)
