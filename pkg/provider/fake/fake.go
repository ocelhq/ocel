package fake

import (
	"context"
	"sync"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/seal"
)

const Vendor provider.Vendor = "fake"

type Provider struct {
	mu                    sync.Mutex
	pins                  map[string]string
	certRefusal           error
	issue                 []edge.Record
	discarded             []string
	servingDiscardRefusal error
	rotation              int
	pending               error
	health                *provider.CertificateHealth
	healthFor             map[string]provider.CertificateHealth
	inspections           inspectionFailures
	probes                probeFailures

	preflightRefusal error
	preflighted      []provider.DeployPreflight
	wrappedFor       []string
	runtimeArch      string
	runtimeBinary    []byte
	hooks            provider.Hooks
	setFacts         func(*provider.Facts)

	journal        *Journal
	options        Options
	keyValues      *KeyValues
	artifacts      provider.ArtifactStore
	images         *Images
	cipher         *Cipher
	bootstrap      *Bootstrap
	stacks         *Stacks
	resourceStacks provider.Stacks
	creds          *Credentials
	edges          *Edges
	routers        *Routers
	dns            *DNS
	connector      *Connector
}

func New(_ context.Context, settings provider.Settings) (provider.Provider, error) {
	decoded, err := provider.Decode[Options](Vendor, settings.Options)
	if err != nil {
		return nil, err
	}
	return NewForProject(decoded, settings.ProjectDir), nil
}

func NewForProject(options Options, projectDir string) *Provider {
	p := NewProvider(options).WithProjectDir(projectDir)
	return p.WithHooks(p.everyHook)
}

func NewProvider(options Options) *Provider {
	journal := &Journal{}
	store := NewKeyValues()
	store.journal = journal
	edges := NewEdges()
	artifacts := NewArtifacts()
	artifacts.journal = journal
	p := &Provider{
		journal:   journal,
		options:   options,
		keyValues: store,
		artifacts: artifacts,
		images:    NewImages(),
		cipher:    NewCipher(),
		bootstrap: NewBootstrap(),
		stacks:    NewStacks(artifacts).journalling(journal),
		creds:     NewCredentials(options.Region),
		edges:     edges,
		routers:   newRouters(edges),
		dns:       NewDNS(),
		connector: &Connector{},

		runtimeArch:   "amd64",
		runtimeBinary: []byte(RuntimeBinary),
	}
	p.hooks = provider.Hooks{
		ProgramEdge:        p.ProgramEdge,
		OpenRegistryImages: p.OpenRegistryImages,
		Cost:               &provider.CostHooks{Shape: p.ShapeCost, Estimate: p.EstimateCost},
	}
	return p
}

func (p *Provider) Hooks() provider.Hooks {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hooks
}

func (p *Provider) Facts() provider.Facts {
	facts := provider.Facts{
		Vendor:          Vendor,
		Bindings:        []provider.BindingType{provider.BindingPostgres, provider.BindingBucket},
		Computes:        []provider.Compute{provider.ComputeServerless, provider.ComputeContainer},
		Edges:           p.edges.kinds(),
		DefaultEdge:     KindRelay,
		Pairings:        p.edges.pairings(),
		DNSKinds:        []provider.DNSKind{KindZone},
		StoresArtifacts: true,
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.setFacts != nil {
		p.setFacts(&facts)
	}
	return facts
}

func (p *Provider) Bootstrap(kind edge.Kind) (provider.Bootstrap, error) {
	if _, err := p.edges.Open(kind); err != nil {
		return nil, err
	}
	p.bootstrap.setDefaultEdge(kind)
	return p.bootstrap, nil
}

func (p *Provider) Stacks() provider.Stacks {
	if p.resourceStacks != nil {
		return p.resourceStacks
	}
	return p.stacks
}

func (p *Provider) Artifacts() provider.ArtifactStore { return p.artifacts }

func (p *Provider) KeyValues() keyvalue.Store { return p.keyValues }

func (p *Provider) Cipher() seal.Cipher { return p.cipher }

func (p *Provider) Credentials() provider.Credentials { return p.creds }

func (p *Provider) Edges() provider.Edges { return p.edges }

func (p *Provider) Routers() provider.Routers { return p.routers }

func (p *Provider) DNS() provider.DNS { return p.dns }

func (p *Provider) Certificates() provider.Certificates { return certificates{p} }

func (p *Provider) Connector() provider.Connector { return p.connector }

func (p *Provider) Runtime() provider.Runtime { return containerRuntime{p} }

func (p *Provider) Liveness() provider.Liveness { return liveness{p} }
