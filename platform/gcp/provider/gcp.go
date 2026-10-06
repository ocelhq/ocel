package gcp

import (
	"context"
	"crypto/x509"
	"slices"
	"strings"
	"sync"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/liveness"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/seal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/cloudrun"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

const Vendor provider.Vendor = "gcp"

type Provider struct {
	options       Options
	tokens        TokenSource
	endpoint      string
	tagEndpoint   string
	tasksEndpoint string
	namespace     provider.Namespace
	warmRoots     *x509.CertPool

	projectDir string

	records keyvalue.Store

	originRoutes originRouting

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
	p.projectDir = settings.ProjectDir
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
	tagEndpoint, err := tagEmulatorEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	tasksEndpoint, err := tasksEmulatorEndpoint(endpoint)
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
		options:       options,
		tokens:        ApplicationDefault{},
		endpoint:      endpoint,
		tagEndpoint:   tagEndpoint,
		tasksEndpoint: tasksEndpoint,
		namespace:     namespace,
		bases:         functionBases(),
		pull:          pullBase,
	}, nil
}

func (p *Provider) Facts() provider.Facts {
	return provider.Facts{
		Vendor:   Vendor,
		Bindings: resources.ServedBindingTypes(p.resourceHooks()),
		Computes: []provider.Compute{provider.ComputeServerless, provider.ComputeContainer},
		Edges:    slices.Clone(supportedEdges),
		Pairings: []provider.Pairing{
			{Edge: edge.None, Router: cloudrun.RouterKind, Computes: provider.Computes()},
			{Edge: alb.Kind, Router: router.Kind(alb.Kind), Computes: provider.Computes()},
			{Edge: cloudflare.Kind, Router: router.Kind(cloudflare.Kind), Computes: []provider.Compute{provider.ComputeServerless}},
			{Edge: cloudflare.Kind, Router: router.Kind(alb.Kind), Computes: []provider.Compute{provider.ComputeContainer}, Forwarded: true},
		},
		DNSKinds:               []provider.DNSKind{dnsCloudflare},
		StoresArtifacts:        true,
		WorkerCeilings:         slices.Clone(workerCeilings),
		NextRefreshesByRequest: true,
		NextRuntimeDir:         nextRuntimeDir,
	}
}

func (p *Provider) Hooks() provider.Hooks {
	return provider.Hooks{
		PreflightDeploy:       p.PreflightDeploy,
		ProgramEdge:           p.ProgramEdge,
		EnsureImageRegistry:   p.EnsureImageRegistry,
		OpenDirectImages:      p.OpenDirectImages,
		CheckBucket:           s3store.Check,
		ProveIdentity:         ports.ProveIdentity,
		Cost:                  &provider.CostHooks{Shape: p.ShapeCost, Estimate: p.EstimateCost},
		FunctionImages:        &provider.FunctionImageHooks{ResolveBase: p.ResolveFunctionBase, ReadRuntime: p.ReadFunctionRuntime},
		ReadNextServerRuntime: p.ReadNextServerRuntime,
	}
}

func (p *Provider) resourceHooks() resources.Hooks {
	return resources.Hooks{
		ProvisionPostgres: p.ProvisionPostgres,
		ProvisionBucket:   p.ProvisionBucket,
		ProvisionKV:       p.ProvisionKV,
		ProvisionTopic:    p.ProvisionTopic,
		ProvisionRealtime: p.ProvisionRealtime,
		RecordsWorkers:    true,
		RemoveResource:    p.RemoveResource,
		Functions: &resources.FunctionHooks{
			Provision: p.ProvisionFunctions,
			Remove:    p.RemoveFunctions,
			Shared:    &resources.SharedHooks[provider.Function]{Name: p.NameFunctions, RemoveRevisions: p.RemoveFunctionRevisions},
		},
		Containers: &resources.ContainerHooks{
			Provision: p.ProvisionContainers,
			Remove:    p.RemoveContainers,
			Shared:    &resources.SharedHooks[provider.AppContainer]{Name: p.NameContainers, RemoveRevisions: p.RemoveContainerRevisions},
		},
	}
}

func (p *Provider) Bootstrap(kind edge.Kind) (provider.Bootstrap, error) {
	if _, err := p.Edges().Open(kind, nil); err != nil {
		return nil, err
	}
	return bootstrapGate{p: p}, nil
}

func (p *Provider) Stacks() provider.Stacks {
	return stacks{Stacks: resources.NewHookStacks(p.KeyValues(), p.Artifacts(), p.resourceHooks()), p: p}
}

func (p *Provider) Artifacts() provider.ArtifactStore { return artifacts{p: p} }

func (p *Provider) KeyValues() keyvalue.Store {
	if p.records != nil {
		return p.records
	}
	return keyValues{p: p}
}

func (p *Provider) Cipher() seal.Cipher { return cipher{p: p} }

func (p *Provider) Credentials() provider.Credentials {
	return Credentials{
		Project:   p,
		Region:    p.options.Region,
		Tokens:    p.tokens,
		Endpoint:  p.endpoint,
		Projects:  resourceManager{endpoint: p.endpoint},
		Namespace: p.namespace,
	}
}

func (p *Provider) Edges() provider.Edges { return p.edges() }

func (p *Provider) Routers() provider.Routers { return routers{edges: p.edges()} }

func (p *Provider) DNS() provider.DNS { return dns{} }

func (p *Provider) Certificates() provider.Certificates { return certificates{p} }

func (p *Provider) Connector() provider.Connector { return connector{p} }

func (p *Provider) Runtime() provider.Runtime { return containerRuntime{p} }

func (p *Provider) Liveness() provider.Liveness { return &p.Net }

func (p *Provider) Logs() provider.Logs { return logs{p} }
