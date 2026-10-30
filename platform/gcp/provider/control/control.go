package control

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/liveness"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/seal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/certs"
	"github.com/ocelhq/ocel/platform/gcp/provider/connector"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudrun"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

type Provider struct {
	Options           Options
	Tokens            gcloud.TokenSource
	GoogleCredentials ports.Credentials
	Emulator          string
	TagEmulator       string
	Namespace         provider.Namespace
	SkipChecks        bool
	Records           records.Store
	Clients           *gcloud.Clients

	Engine Engine

	externalAccountFile string

	mu                sync.Mutex
	clientsChecked    bool
	recordedDatabases sync.Map

	liveness.Net
}

func New(ctx context.Context, settings provider.Settings) (*Provider, error) {
	decoded, err := provider.Decode[Options](gcloud.Vendor, settings.Options)
	if err != nil {
		return nil, err
	}
	p, err := NewProvider(decoded)
	if err != nil {
		return nil, err
	}
	if settings.WebIdentity == nil {
		return p, nil
	}
	if err := p.useWebIdentity(ctx, settings.WebIdentity); err != nil {
		return nil, err
	}
	return p, nil
}

func NewProvider(options Options) (*Provider, error) {
	if strings.TrimSpace(options.Region) == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"option %q names no region, and a project spans them all: name the one this deploy runs in", "region")
	}
	emulator, err := emulatorEndpoint()
	if err != nil {
		return nil, err
	}
	tagEmulator, err := tagEmulatorEndpoint(emulator)
	if err != nil {
		return nil, err
	}
	namespace, err := provider.NamespaceFromEnv()
	if err != nil {
		return nil, err
	}
	options.Project = namedProject(options.Project)
	return &Provider{
		Options:     options,
		Tokens:      ports.Credentials{},
		Emulator:    emulator,
		TagEmulator: tagEmulator,
		Namespace:   namespace,
		SkipChecks:  processenv.SkipChecks(),
		Engine:      needsRunner{},
	}, nil
}

func (p *Provider) Facts() provider.Facts {
	return provider.Facts{
		Vendor: gcloud.Vendor,
		Bindings: []provider.BindingType{
			provider.BindingPostgres, provider.BindingBucket, provider.BindingKV,
			provider.BindingTopic, provider.BindingTask, provider.BindingRealtime,
		},
		Computes: []provider.Compute{provider.ComputeServerless, provider.ComputeContainer},
		Edges:    slices.Clone(edges.Supported),
		Pairings: []provider.Pairing{
			{Edge: edge.None, Router: cloudrun.RouterKind, Computes: provider.Computes()},
			{Edge: albresources.Kind, Router: router.Kind(albresources.Kind), Computes: provider.Computes()},
			{Edge: cloudflare.Kind, Router: router.Kind(cloudflare.Kind), Computes: provider.Computes()},
		},
		DNSKinds:          []provider.DNSKind{dnsCloudflare},
		StoresArtifacts:   true,
		RendersTransforms: true,
		WorkerCeilings: []provider.WorkerCeiling{
			{Compute: provider.ComputeServerless, MaxDuration: topics.PushAckDeadline},
			{Compute: provider.ComputeContainer, MaxDuration: topics.PushAckDeadline},
		},

		RunsGates:             true,
		RunsFunctionsAsImages: true,

		WebIdentity: &provider.WebIdentityFacts{CredentialEnvVars: credentialEnvVars},
	}
}

func (p *Provider) Hooks() provider.Hooks {
	return provider.Hooks{
		Preflight:            &provider.PreflightHooks{Plan: refusePlanPreflight, Apply: refuseApplyPreflight},
		InfraPreflight:       &provider.PreflightHooks{Plan: refusePlanInfraPreflight, Apply: refuseApplyInfraPreflight},
		ProgramEdge:          refuseProgramEdge,
		EnsureImageRegistry:  refuseEnsureImageRegistry,
		OpenDirectImages:     refuseOpenDirectImages,
		CheckBucket:          refuseCheckBucket,
		EnvSource:            &provider.EnvSourceHooks{ProveIdentity: p.GoogleCredentials.ProveIdentity, EnsureSync: refuseEnsureEnvSourceSync},
		BindingAccess:        &provider.BindingAccessHooks{ForwardPorts: refuseForwardPorts, ServeProxy: refuseServeBindingProxy},
		Cost:                 &provider.CostHooks{Inventory: refuseInventory, Estimate: EstimateCost},
		Retirement:           &provider.RetirementHooks{Plan: refusePlanRetirement, Retire: refuseRetire},
		ProjectRemoval:       &provider.ProjectRemovalHooks{Plan: refusePlanProjectRemoval, Remove: refuseRemoveProject},
		ListProjectResources: p.ListProjectResources,
	}
}

func (p *Provider) Bootstrap(edge.Kind) (provider.Bootstrap, error) {
	return nil, provider.RefuseNeedsRunner("bootstrapping a tier")
}

func (p *Provider) Stacks() provider.Stacks { return stacks{p: p} }

func (p *Provider) Artifacts() provider.ArtifactStore { return p.Objects() }

func (p *Provider) KeyValues() records.Store {
	if p.Records != nil {
		return p.Records
	}
	return KeyValues{Provider: p}
}

func (p *Provider) Cipher() seal.Cipher { return cipher{p: p} }

func (p *Provider) Credentials() provider.Credentials {
	return Credentials{
		Project:   p,
		Region:    p.Options.Region,
		Tokens:    p.Tokens,
		Endpoint:  p.Emulator,
		Projects:  resourceManager{endpoint: p.Emulator, credentials: p.GoogleCredentials},
		Namespace: p.Namespace,
	}
}

func (p *Provider) Edges() provider.Edges { return p.EdgeRegistry(p.computeEngine) }

func (p *Provider) Routers() provider.Routers {
	return edges.Routers{Registry: p.EdgeRegistry(p.computeEngine)}
}

func (p *Provider) DNS() provider.DNS {
	return dns{cloudflare: p.EdgeRegistry(p.computeEngine).Cloudflare()}
}

func (p *Provider) Certificates() provider.Certificates { return certs.New(p.OpenClients) }

func (p *Provider) Connector() provider.Connector {
	return connectorPort{connector.New(p.OpenClients, p.Services(), p.Options.Region, p.Emulated(), nil, nil, nil)}
}

func (p *Provider) Runtime() provider.Runtime { return provider.AbsentEngine{} }

func (p *Provider) Liveness() provider.Liveness { return &p.Net }

func (p *Provider) Logs() provider.Logs { return logs{p} }
