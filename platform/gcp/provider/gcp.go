package gcp

import (
	"context"
	"crypto/x509"
	"slices"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/liveness"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/seal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/certs"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/release"
	"github.com/ocelhq/ocel/platform/gcp/provider/connector"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudrun"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

type Provider struct {
	options     Options
	tokens      gcloud.TokenSource
	endpoint    string
	gateInEnv   bool
	tagEndpoint string
	namespace   provider.Namespace
	warmRoots   *x509.CertPool

	writtenBy  provider.WrittenBy
	skipChecks bool
	transforms transform.Pass

	records           records.Store
	recordedDatabases sync.Map

	mu             sync.Mutex
	resolved       *gcloud.Clients
	clientsChecked bool
	surveyed       bootstrap.Surveys

	images *run.Images

	liveness.Net
}

func New(_ context.Context, settings provider.Settings) (provider.Provider, error) {
	decoded, err := provider.Decode[Options](gcloud.Vendor, settings.Options)
	if err != nil {
		return nil, err
	}
	p, err := NewProvider(decoded)
	if err != nil {
		return nil, err
	}
	p.writtenBy = settings.WrittenBy
	p.transforms = transformPass(settings.ProjectDir, settings.Transforms)
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
	namespace, err := provider.NamespaceFromEnv()
	if err != nil {
		return nil, err
	}
	options.Project = namedProject(options.Project)
	return &Provider{
		options:     options,
		tokens:      gcloud.ApplicationDefault{},
		endpoint:    endpoint,
		gateInEnv:   endpoint != "",
		tagEndpoint: tagEndpoint,
		namespace:   namespace,
		images:      &run.Images{Pull: run.PullBase},
		skipChecks:  processenv.SkipChecks(),
	}, nil
}

func (p *Provider) Facts() provider.Facts {
	return provider.Facts{
		Vendor:   gcloud.Vendor,
		Bindings: resources.ServedBindingTypes(p.resourceHooks()),
		Computes: []provider.Compute{provider.ComputeServerless, provider.ComputeContainer},
		Edges:    slices.Clone(edges.Supported),
		Pairings: []provider.Pairing{
			{Edge: edge.None, Router: cloudrun.RouterKind, Computes: provider.Computes()},
			{Edge: alb.Kind, Router: router.Kind(alb.Kind), Computes: provider.Computes()},
			{Edge: cloudflare.Kind, Router: router.Kind(cloudflare.Kind), Computes: provider.Computes()},
		},
		DNSKinds:          []provider.DNSKind{dnsCloudflare},
		StoresArtifacts:   true,
		RendersTransforms: true,
		WorkerCeilings:    slices.Clone(release.WorkerCeilings),

		RunsGates:             true,
		RunsFunctionsAsImages: true,
	}
}

func (p *Provider) Hooks() provider.Hooks {
	return provider.Hooks{
		Preflight:            &provider.PreflightHooks{Plan: p.PlanPreflight, Apply: p.ApplyPreflight},
		InfraPreflight:       &provider.PreflightHooks{Plan: p.PlanInfraPreflight, Apply: p.ApplyInfraPreflight},
		ProgramEdge:          p.ProgramEdge,
		EnsureImageRegistry:  p.EnsureImageRegistry,
		OpenDirectImages:     p.OpenDirectImages,
		CheckBucket:          s3store.Check,
		NextServerRuntime:    nextServerRuntime,
		EnvSource:            &provider.EnvSourceHooks{ProveIdentity: ports.ProveIdentity, EnsureSync: p.EnsureEnvSourceSync},
		BindingAccess:        &provider.BindingAccessHooks{ForwardPorts: p.ForwardPorts, ServeProxy: p.ServeBindingProxy},
		Cost:                 &provider.CostHooks{Shape: p.ShapeCost, Estimate: p.EstimateCost},
		Retirement:           &provider.RetirementHooks{Plan: p.PlanRetirement, Retire: p.Retire},
		ProjectRemoval:       &provider.ProjectRemovalHooks{Plan: p.PlanProjectRemoval, Remove: p.RemoveProject},
		ListProjectResources: p.ListProjectResources,
	}
}

func (p *Provider) Bootstrap(kind edge.Kind) (provider.Bootstrap, error) {
	if _, err := p.Edges().Open(kind, nil); err != nil {
		return nil, err
	}
	return bootstrapGate{p: p}, nil
}

func (p *Provider) Stacks() provider.Stacks {
	return stacks{Stacks: resources.NewHookStacks(p.KeyValues(), p.resourceHooks()), p: p}
}

func (p *Provider) Artifacts() provider.ArtifactStore { return p.objects() }

func (p *Provider) KeyValues() records.Store {
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

func (p *Provider) Routers() provider.Routers { return edges.Routers{Registry: p.edges()} }

func (p *Provider) DNS() provider.DNS {
	return dns{cloudflare: p.edges().Cloudflare()}
}

func (p *Provider) Certificates() provider.Certificates { return certs.New(p.openClients) }

func (p *Provider) Connector() provider.Connector {
	return connector.New(p.openClients, p.services(), p.options.Region, p.emulated(), p.images.Based, p.pushImage,
		func(ctx context.Context, tier environment.Tier) error { return p.ensureKeys(ctx, tier, "") })
}

func (p *Provider) Runtime() provider.Runtime { return containerRuntime{p} }

func (p *Provider) Liveness() provider.Liveness { return &p.Net }

func (p *Provider) Logs() provider.Logs { return logs{p} }
