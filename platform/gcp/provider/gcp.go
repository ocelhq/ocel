package gcp

import (
	"context"
	"slices"
	"strings"
	"sync"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/liveness"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/direct"
)

const Vendor provider.Vendor = "gcp"

type Provider struct {
	options   Options
	tokens    TokenSource
	endpoint  string
	namespace provider.Namespace

	mu       sync.Mutex
	resolved *clients

	bases  map[string]base
	pull   func(ctx context.Context, ref string) (v1.Image, error)
	pulled sync.Map

	liveness.Net
}

func New(_ context.Context, settings provider.Settings) (provider.Provider, error) {
	decoded, err := provider.Decode[Options](Vendor, settings.Options)
	if err != nil {
		return nil, err
	}
	p, err := NewProvider(decoded)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func NewProvider(options Options) (*Provider, error) {
	if strings.TrimSpace(options.Region) == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"option %q names no region, and a project spans them all: name the one this deploy runs in", "region")
	}
	endpoint, err := emulatorEndpoint()
	if err != nil {
		return nil, err
	}
	namespace, err := provider.NamespaceFromEnv()
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

func (p *Provider) Facts() provider.Facts {
	return provider.Facts{
		Vendor:          Vendor,
		Bindings:        resources.ServedBindingTypes(p.resourceHooks()),
		Computes:        []provider.Compute{provider.ComputeServerless, provider.ComputeContainer},
		Edges:           slices.Clone(supportedEdges),
		DefaultEdge:     direct.Kind,
		DNSKinds:        []provider.DNSKind{dnsCloudflare},
		StoresArtifacts: true,
	}
}

func (p *Provider) Hooks() provider.Hooks {
	return provider.Hooks{
		EnsureImageRegistry: p.EnsureImageRegistry,
		OpenDirectImages:    p.OpenDirectImages,
		Cost:                &provider.CostHooks{Shape: p.ShapeCost, Estimate: p.EstimateCost},
		FunctionImages:      &provider.FunctionImageHooks{ResolveBase: p.ResolveFunctionBase, ReadRuntime: p.ReadFunctionRuntime},
	}
}

func (p *Provider) resourceHooks() resources.Hooks {
	return resources.Hooks{
		Functions:  &resources.FunctionHooks{Provision: p.ProvisionFunctions, Remove: p.RemoveFunctions},
		Containers: &resources.ContainerHooks{Provision: p.ProvisionContainers, Remove: p.RemoveContainers},
	}
}

func (p *Provider) Bootstrap(kind edge.Kind) (provider.Bootstrap, error) {
	if _, err := p.Edges().Open(kind); err != nil {
		return nil, err
	}
	return bootstrapGate{p: p}, nil
}

func (p *Provider) Stacks() provider.Stacks {
	return resources.NewHookStacks(p.Records(), p.Artifacts(), p.resourceHooks())
}

func (p *Provider) Artifacts() provider.ArtifactStore { return artifacts{p: p} }

func (p *Provider) Records() records.Store { return recordStore{p: p} }

func (p *Provider) Cipher() records.Cipher { return cipher{p: p} }

func (p *Provider) Credentials() provider.Credentials {
	return Credentials{
		Project:  p,
		Region:   p.options.Region,
		Tokens:   p.tokens,
		Endpoint: p.endpoint,
		Projects: resourceManager{endpoint: p.endpoint},
	}
}

func (p *Provider) Edges() provider.Edges {
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

func (p *Provider) DNS() provider.DNS { return dns{} }

func (p *Provider) Certificates() provider.Certificates { return certificates{p} }

func (p *Provider) Connector() provider.Connector { return connector{p} }

func (p *Provider) Runtime() images.Runtime { return containerRuntime{p} }

func (p *Provider) Liveness() provider.Liveness { return &p.Net }
