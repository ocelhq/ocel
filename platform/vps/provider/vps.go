package vps

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/liveness"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	s3store "github.com/ocelhq/ocel/platform/s3"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const Vendor provider.Vendor = "vps"

type Provider struct {
	options   Options
	project   string
	host      *host.Host
	keyValues keyvalue.Store
	cipher    *boxstore.Cipher

	transform transform.Pass
	resolve   Lookup
	reaches   Reach
	now       func() time.Time

	stores liveStores
	queues queueDatabases

	dial sync.Mutex
	live *session.Session

	liveness.Net
}

func New(_ context.Context, settings provider.Settings) (provider.Provider, error) {
	decoded, err := provider.Decode[Options](Vendor, settings.Options)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(decoded.SSH.Alias) == "" && strings.TrimSpace(decoded.SSH.Host) == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid, "option %q names no machine: give it an ssh_config alias or an object with a host", "ssh")
	}
	if port := decoded.SSH.Port; port != 0 && (port < 1 || port > 65535) {
		return nil, refusal.Refuse(refusal.CodeInvalid, "option %q names port %d, which is outside 1-65535", "ssh", port)
	}
	if err := decoded.Proxy.usable(decoded.Certificates); err != nil {
		return nil, err
	}
	p := NewProvider(decoded)
	p.project = settings.Slug
	if len(settings.Transforms) > 0 {
		p.transform = nodePass(settings.ProjectDir, settings.Transforms)
	}
	return p, nil
}

func NewProvider(options Options) *Provider {
	p := &Provider{options: options, now: time.Now}
	return p.onHost(p.conn)
}

func newProvider(options Options, dial host.Dial) *Provider {
	return (&Provider{options: options, now: time.Now}).onHost(dial)
}

func (p *Provider) Facts() provider.Facts {
	return provider.Facts{
		Vendor:   Vendor,
		Bindings: resources.ServedBindingTypes(p.resourceHooks()),
		Computes: []provider.Compute{provider.ComputeContainer},
		Edges:    slices.Clone(supportedEdges),
		Pairings: []provider.Pairing{
			{Edge: edge.None, Router: switchboard.RouterKind, Computes: []provider.Compute{provider.ComputeContainer}},
			{Edge: cloudflare.Kind, Router: switchboard.RouterKind, Computes: []provider.Compute{provider.ComputeContainer}},
		},
		DNSKinds:                 []provider.DNSKind{dnsCloudflare},
		RendersTransforms:        true,
		RunsTunnels:              true,
		RetainsContainerReleases: true,
		WorkerCeilings:           []provider.WorkerCeiling{{Compute: provider.ComputeContainer, Unbounded: true}},
	}
}

func (p *Provider) Hooks() provider.Hooks {
	return provider.Hooks{
		PreflightDeploy:    p.PreflightDeploy,
		OpenRegistryImages: p.OpenRegistryImages,
		OpenDirectImages:   p.OpenDirectImages,
		CheckHost:          p.CheckHost,
		CheckBucket:        s3store.Check,
	}
}

func (p *Provider) resourceHooks() resources.Hooks {
	return resources.Hooks{
		ProvisionPostgres: p.ProvisionPostgres,
		ProvisionBucket:   p.ProvisionBucket,
		ProvisionKV:       p.ProvisionKV,
		ProvisionTopic:    p.ProvisionTopic,
		RemoveResource:    p.RemoveResource,
		Containers:        &resources.ContainerHooks{Provision: p.ProvisionContainers, Remove: p.RemoveContainers},
		Retention:         &resources.ImageRetentionHooks{Reconcile: p.ReconcileImages, Forget: p.ForgetReleases},
	}
}

func (p *Provider) Bootstrap(kind edge.Kind) (provider.Bootstrap, error) {
	bootstrap := elevating{Bootstrap: host.NewBootstrap(p.host, Vendor, p.project), elevated: p.elevated}
	if kind == edge.None {
		bootstrap.served = p.refuseServingPortsClosedFromOutside
	}
	return bootstrap, nil
}

func (p *Provider) Stacks() provider.Stacks {
	return resources.NewHookStacks(p.keyValues, p.Artifacts(), p.resourceHooks())
}

func (p *Provider) Artifacts() provider.ArtifactStore { return resources.NoArtifacts{} }

func (p *Provider) KeyValues() keyvalue.Store { return p.keyValues }

func (p *Provider) Cipher() seal.Cipher { return p.cipher }

func (p *Provider) Credentials() provider.Credentials { return credentials{p} }

func (p *Provider) Edges() provider.Edges { return edges{p} }

func (p *Provider) Routers() provider.Routers { return routers{p} }

func (p *Provider) DNS() provider.DNS { return dns{} }

func (p *Provider) Certificates() provider.Certificates { return certificates{p} }

func (p *Provider) Connector() provider.Connector { return connector{p} }

func (p *Provider) Runtime() provider.Runtime { return containerRuntime{p} }

func (p *Provider) Liveness() provider.Liveness { return &p.Net }
