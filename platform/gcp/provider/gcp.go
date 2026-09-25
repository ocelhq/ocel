package gcp

import (
	"context"
	"strings"
	"sync"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/payloads"
)

const Vendor providerkit.Vendor = "gcp"

type Options struct {
	Project string `json:"project" doc:"The Google Cloud project to deploy into."`
	Region  string `json:"region" doc:"The region to deploy into. A project spans them all, so this names the one."`
}

type Provider struct {
	options   Options
	tokens    TokenSource
	endpoint  string
	namespace providerkit.Namespace

	mu       sync.Mutex
	standing *clients

	bases  map[string]base
	pull   func(ctx context.Context, ref string) (v1.Image, error)
	pulled sync.Map

	providerkit.Liveness
}

func New(_ context.Context, settings providerkit.Settings) (providerkit.Provider, error) {
	decoded, err := providerkit.Decode[Options](Vendor, settings.Options)
	if err != nil {
		return nil, err
	}
	provider, err := NewProvider(decoded)
	if err != nil {
		return nil, err
	}
	return provider, nil
}

func NewProvider(options Options) (*Provider, error) {
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
	options.Project = namedProject(options.Project)
	if options.Project != "" {
		if err := (Names{namespace: namespace, project: options.Project}).fit(); err != nil {
			return nil, err
		}
	}
	return &Provider{
		options:   options,
		tokens:    ApplicationDefault{},
		endpoint:  endpoint,
		namespace: namespace,
		bases:     functionBases(),
		pull:      pullBase,
	}, nil
}

func (p *Provider) stood(ctx context.Context) (*clients, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.standing != nil {
		return p.standing, nil
	}
	project := p.options.Project
	if project == "" {
		ambient, err := ambientProject(ctx)
		if err != nil {
			return nil, err
		}
		project = ambient
	}
	names := Names{namespace: p.namespace, project: project}
	if err := names.fit(); err != nil {
		return nil, err
	}
	p.standing = &clients{Names: names, region: p.options.Region, endpoint: p.endpoint}
	return p.standing, nil
}

func (p *Provider) Names(ctx context.Context) (Names, error) {
	held, err := p.stood(ctx)
	if err != nil {
		return Names{}, err
	}
	return held.Names, nil
}

func (p *Provider) Project(ctx context.Context) (string, error) {
	names, err := p.Names(ctx)
	if err != nil {
		return "", err
	}
	return names.project, nil
}

func (p *Provider) named() (Names, error) {
	if p.options.Project == "" {
		return Names{}, unnamedProject()
	}
	return Names{namespace: p.namespace, project: p.options.Project}, nil
}

func (p *Provider) emulated() bool { return p.endpoint != "" }

func (p *Provider) Region() string { return p.options.Region }

func (p *Provider) Facts() providerkit.Facts {
	return providerkit.Facts{
		Vendor:   Vendor,
		Bindings: resources.Serves(p),
		Computes: []providerkit.Compute{providerkit.ComputeServerless, providerkit.ComputeContainer},
	}
}

func (p *Provider) Bootstrap(kind edge.Kind) (providerkit.Bootstrapper, error) {
	if _, err := p.Edges().Open(kind); err != nil {
		return nil, err
	}
	return bootstrapGate{p: p}, nil
}

func (p *Provider) Releases() providerkit.Releaser {
	return resources.Releaser(p.Records(), p.Artifacts(), p)
}

func (p *Provider) Artifacts() providerkit.ArtifactStore { return artifacts{p: p} }

func (p *Provider) ContainerArch(_ context.Context, app, declared string) (string, error) {
	if runs, _ := providerkit.GoArch(declared); runs != payloads.ContainerArch {
		return "", providerkit.Refuse(providerkit.CodeInvalid,
			"app %s declares arch %q, and Cloud Run runs %s alone: drop the arch, or deploy %s to a provider that runs %s",
			app, declared, providerkit.ArchX8664, app, declared)
	}
	return payloads.ContainerArch, nil
}

func (p *Provider) ContainerRuntime(_ context.Context, arch string) ([]byte, error) {
	return payloads.ContainerRuntime(arch)
}

func (p *Provider) Records() providerkit.RecordStore { return records{p: p} }

func (p *Provider) Sealer() providerkit.Sealer { return sealer{p: p} }

func (p *Provider) Credentials() providerkit.Credentials {
	return Credentials{
		Project:  p,
		Region:   p.options.Region,
		Tokens:   p.tokens,
		Endpoint: p.endpoint,
		Projects: resourceManager{endpoint: p.endpoint},
	}
}

func (p *Provider) Edges() providerkit.EdgeRegistry {
	return edges{
		namespace: p.namespace,
		records:   p.Records(),
		pins:      p,
		stacks:    albStacks{p: p},
		routes:    p,
		entries:   p,
		project:   p.options.Project,
		region:    p.options.Region,
	}
}

func (p *Provider) DNS() providerkit.DNSRegistry { return dns{} }

var (
	_ providerkit.Provider          = (*Provider)(nil)
	_ providerkit.Diagnoser         = (*Provider)(nil)
	_ providerkit.ContainerRuntimer = (*Provider)(nil)
)
