package fake

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"

	"connectrpc.com/connect"
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
	runtimeArch      string
	runtimeBinary    []byte
	hooks            providerkit.Hooks

	journal   *Journal
	options   Options
	records   *Records
	artifacts providerkit.ArtifactStore
	images    *Images
	sealer    *Cipher
	bootstrap *Bootstrap
	releases  *Stacks
	releasing providerkit.Stacks
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
		sealer:    NewCipher(),
		bootstrap: NewBootstrap(),
		releases:  NewStacks(artifacts).journalling(journal),
		creds:     NewCredentials(options.Region),
		edges:     NewEdges(records),
		dns:       NewDNS(),

		runtimeArch:   "amd64",
		runtimeBinary: []byte(RuntimeBinary),
	}
	p.hooks = providerkit.Hooks{
		ProgramEdge:        p.ProgramEdge,
		OpenRegistryImages: p.OpenRegistryImages,
		Cost:               &providerkit.CostHooks{Shape: p.ShapeCost, Estimate: p.EstimateCost},
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
	hooks.WarmFunctions = func(context.Context, []string, providerkit.Progress) error { return nil }
	hooks.EmbedCode = func(context.Context, string, providerkit.ArtifactRef, providerkit.Progress) error { return nil }
	hooks.InspectStack = p.InspectStack
	hooks.VerifyGrants = func(context.Context, providerkit.Binding) error { return nil }
	hooks.PreflightDeploy = p.PreflightDeploy
	hooks.EnsureImageRegistry = p.EnsureImageRegistry
}

func (p *Provider) ResourceHooks() resources.Hooks {
	return resources.Hooks{
		Functions:  &resources.FunctionHooks{Provision: p.ProvisionFunctions, Remove: p.RemoveFunctions},
		Containers: &resources.ContainerHooks{Provision: p.ProvisionContainers, Remove: p.RemoveContainers},
	}
}

func (p *Provider) Ships(store providerkit.ArtifactStore) *Provider {
	p.artifacts = store
	p.releases.artifacts = store
	return p
}

func (p *Provider) OpenRegistryImages(_ context.Context, target providerkit.RegistryTarget) (providerkit.ImageStore, error) {
	p.images.open(target)
	return p.images, nil
}

func (p *Provider) Registry() *Images { return p.images }

func (p *Provider) Facts() providerkit.Facts {
	return providerkit.Facts{
		Vendor:          Vendor,
		Bindings:        []providerkit.BindingType{providerkit.BindingPostgres, providerkit.BindingBucket},
		Computes:        []providerkit.Compute{providerkit.ComputeServerless, providerkit.ComputeContainer},
		Edges:           p.edges.kinds(),
		DefaultEdge:     KindRelay,
		DNSKinds:        []providerkit.DNSKind{KindZone},
		StoresArtifacts: true,
	}
}

func (p *Provider) Region() string { return p.options.Region }

func (p *Provider) Bootstrap(kind edge.Kind) (providerkit.Bootstrap, error) {
	if _, err := p.edges.Open(kind); err != nil {
		return nil, err
	}
	p.bootstrap.fronting(kind)
	return p.bootstrap, nil
}

func (p *Provider) FakeBootstrap() *Bootstrap { return p.bootstrap }

func (p *Provider) Journal() []string { return p.journal.Entries() }

func (p *Provider) Releasing(hooks resources.Hooks) *Provider {
	p.releasing = resources.Stacks(p.records, p.artifacts, hooks)
	return p
}

func (p *Provider) Stacks() providerkit.Stacks {
	if p.releasing != nil {
		return p.releasing
	}
	return p.releases
}

func (p *Provider) FakeStacks() *Stacks { return p.releases }

func (p *Provider) Artifacts() providerkit.ArtifactStore { return p.artifacts }

func (p *Provider) Records() providerkit.RecordStore { return p.records }

func (p *Provider) Cipher() providerkit.Cipher { return p.sealer }

func (p *Provider) Credentials() providerkit.Credentials { return p.creds }

func (p *Provider) Edges() providerkit.Edges { return p.edges }

func (p *Provider) DNS() providerkit.DNS { return p.dns }

func (p *Provider) Certificates() providerkit.Certificates { return certificates{p} }

func (p *Provider) Connector() providerkit.Connector { return connector{} }

func (p *Provider) Runtime() providerkit.Runtime { return containerRuntime{p} }

func (p *Provider) Liveness() providerkit.Liveness { return liveness{p} }

type liveness struct{ *Provider }

func (p liveness) ServingEdge(_ context.Context, _ edge.Kind, hostname string) (edge.Kind, error) {
	return p.edges.answering(hostname), nil
}

func (liveness) Unreached(string) string { return "" }

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

type certificates struct{ *Provider }

func (p certificates) Issue(ctx context.Context, req providerkit.CertificateRequest) (providerkit.Certificate, error) {
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
	if req.Current.Requested && req.Current.ID == cert.ID {
		return req.Current, nil
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

func (p certificates) Inspect(_ context.Context, _ edge.Kind, hostname string, cert providerkit.Certificate) (providerkit.CertificateHealth, error) {
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

func (p certificates) Discard(_ context.Context, cert providerkit.Certificate, _ providerkit.Progress) error {
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

const RuntimeBinary = "the fake container runtime"

func (p *Provider) WrappingContainers(arch string, binary []byte) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.runtimeArch, p.runtimeBinary = arch, binary
	return p
}

type containerRuntime struct{ *Provider }

func (r containerRuntime) Arch(_ context.Context, _, declared string) (string, error) {
	if declared == "" {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.runtimeArch, nil
	}
	runs, _ := providerkit.GoArch(declared)
	return runs, nil
}

func (r containerRuntime) Binary(_ context.Context, arch string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wrappedFor = append(r.wrappedFor, arch)
	return r.runtimeBinary, nil
}

type connector struct{}

var errNoConnector = connect.NewError(connect.CodeUnimplemented,
	errors.New("this provider puts no connector on its targets; the console reaches a target of this kind through the provider itself"))

func (connector) Target(context.Context) (providerkit.ConnectorTarget, error) {
	return providerkit.ConnectorTarget{}, errNoConnector
}

func (connector) Install(context.Context, providerkit.ConnectorInstall, providerkit.Progress) (providerkit.ConnectorAddress, error) {
	return providerkit.ConnectorAddress{}, errNoConnector
}

func (connector) Remove(context.Context, providerkit.Progress) error { return errNoConnector }

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

func (*Provider) ProvisionFunctions(_ context.Context, plan providerkit.StackPlan, _ providerkit.Progress) ([]providerkit.Function, error) {
	return StoodUpFunctions(plan), nil
}

func (p *Provider) RemoveFunctions(_ context.Context, _ providerkit.StackRef, functions []providerkit.Function, _ providerkit.Progress) error {
	for _, function := range functions {
		p.releases.tookDown(function.Name)
	}
	return nil
}

func (*Provider) ProvisionContainers(_ context.Context, plan providerkit.StackPlan, _ providerkit.Progress) ([]providerkit.AppContainer, error) {
	return StoodUpContainers(plan), nil
}

func (p *Provider) RemoveContainers(_ context.Context, _ providerkit.StackRef, containers []providerkit.AppContainer, _ providerkit.Progress) error {
	for _, container := range containers {
		p.releases.tookDown(container.Name)
	}
	return nil
}

const (
	RegistryServer    = "registry.fake.invalid"
	RegistryNamespace = "ocel"
)
