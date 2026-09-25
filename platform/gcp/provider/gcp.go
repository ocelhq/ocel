package gcp

import (
	"context"
	"slices"
	"strings"
	"sync"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/direct"
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

	providerkit.NetLiveness
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

func (p *Provider) Facts() providerkit.Facts {
	return providerkit.Facts{
		Vendor:          Vendor,
		Bindings:        resources.Serves(p.resourceHooks()),
		Computes:        []providerkit.Compute{providerkit.ComputeServerless, providerkit.ComputeContainer},
		Edges:           slices.Clone(supportedEdges),
		DefaultEdge:     direct.Kind,
		DNSKinds:        []providerkit.DNSKind{dnsCloudflare},
		StoresArtifacts: true,
	}
}

func (p *Provider) Hooks() providerkit.Hooks {
	return providerkit.Hooks{
		EnsureImageRegistry: p.EnsureImageRegistry,
		OpenDirectImages:    p.OpenDirectImages,
		Cost:                &providerkit.CostHooks{Shape: p.ShapeCost, Estimate: p.EstimateCost},
		FunctionImages:      &providerkit.FunctionImageHooks{ResolveBase: p.ResolveFunctionBase, ReadRuntime: p.ReadFunctionRuntime},
	}
}

func (p *Provider) resourceHooks() resources.Hooks {
	return resources.Hooks{
		Functions:  &resources.FunctionHooks{Provision: p.ProvisionFunctions, Remove: p.RemoveFunctions},
		Containers: &resources.ContainerHooks{Provision: p.ProvisionContainers, Remove: p.RemoveContainers},
	}
}

func (p *Provider) Bootstrap(kind edge.Kind) (providerkit.Bootstrap, error) {
	if _, err := p.Edges().Open(kind); err != nil {
		return nil, err
	}
	return bootstrapGate{p: p}, nil
}

func (p *Provider) Stacks() providerkit.Stacks {
	return resources.Stacks(p.Records(), p.Artifacts(), p.resourceHooks())
}

func (p *Provider) Artifacts() providerkit.ArtifactStore { return artifacts{p: p} }

func (p *Provider) Records() providerkit.RecordStore { return records{p: p} }

func (p *Provider) Cipher() providerkit.Cipher { return cipher{p: p} }

func (p *Provider) Credentials() providerkit.Credentials {
	return Credentials{
		Project:  p,
		Region:   p.options.Region,
		Tokens:   p.tokens,
		Endpoint: p.endpoint,
		Projects: resourceManager{endpoint: p.endpoint},
	}
}

func (p *Provider) Edges() providerkit.Edges {
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

func (p *Provider) DNS() providerkit.DNS { return dns{} }

func (p *Provider) Certificates() providerkit.Certificates { return certificates{p} }

func (p *Provider) Connector() providerkit.Connector { return connector{p} }

func (p *Provider) Runtime() providerkit.Runtime { return containerRuntime{p} }

func (p *Provider) Liveness() providerkit.Liveness { return &p.NetLiveness }
