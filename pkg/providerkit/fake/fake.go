package fake

import (
	"context"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const Vendor providerkit.Vendor = "fake"

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

	journal        *Journal
	options        Options
	records        *Records
	artifacts      providerkit.ArtifactStore
	images         *Images
	cipher         *Cipher
	bootstrap      *Bootstrap
	stacks         *Stacks
	resourceStacks providerkit.Stacks
	creds          *Credentials
	edges          *Edges
	dns            *DNS
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
		cipher:    NewCipher(),
		bootstrap: NewBootstrap(),
		stacks:    NewStacks(artifacts).journalling(journal),
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

func (p *Provider) Bootstrap(kind edge.Kind) (providerkit.Bootstrap, error) {
	if _, err := p.edges.Open(kind); err != nil {
		return nil, err
	}
	p.bootstrap.fronting(kind)
	return p.bootstrap, nil
}

func (p *Provider) Stacks() providerkit.Stacks {
	if p.resourceStacks != nil {
		return p.resourceStacks
	}
	return p.stacks
}

func (p *Provider) Artifacts() providerkit.ArtifactStore { return p.artifacts }

func (p *Provider) Records() records.Store { return p.records }

func (p *Provider) Cipher() records.Cipher { return p.cipher }

func (p *Provider) Credentials() providerkit.Credentials { return p.creds }

func (p *Provider) Edges() providerkit.Edges { return p.edges }

func (p *Provider) DNS() providerkit.DNS { return p.dns }

func (p *Provider) Certificates() providerkit.Certificates { return certificates{p} }

func (p *Provider) Connector() providerkit.Connector { return connector{} }

func (p *Provider) Runtime() images.Runtime { return containerRuntime{p} }

func (p *Provider) Liveness() providerkit.Liveness { return liveness{p} }
