package vps

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/liveness"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	s3store "github.com/ocelhq/ocel/platform/s3"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const Vendor provider.Vendor = "vps"

type Provider struct {
	options   Options
	project   string
	host      *host.Host
	keyValues records.Store
	cipher    *boxstore.Cipher

	transform transform.Pass
	resolve   Lookup
	reaches   Reach
	now       func() time.Time

	stores liveStores
	queues queueDatabases

	dial sync.Mutex
	live *ssh.Session

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

func newProvider(options Options, dial ssh.Dial) *Provider {
	return (&Provider{options: options, now: time.Now}).onHost(dial)
}

func (p *Provider) Facts() provider.Facts {
	return provider.Facts{
		Vendor:   Vendor,
		Bindings: resources.ServedBindingTypes(p.resourceHooks()),
		Computes: []provider.Compute{provider.ComputeContainer},
		Edges:    slices.Clone(supportedEdges),
		Pairings: []provider.Pairing{
			{Edge: edge.None, Router: boxapi.SwitchboardRouter, Computes: []provider.Compute{provider.ComputeContainer}},
			{Edge: cloudflare.Kind, Router: boxapi.SwitchboardRouter, Computes: []provider.Compute{provider.ComputeContainer}},
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
		CheckBucket:        s3store.Check,
		BindingAccess:      &provider.BindingAccessHooks{ForwardPorts: p.ForwardPorts, ServeProxy: p.ServeBindingProxy},
		Retirement:         &provider.RetirementHooks{Plan: p.PlanRetirement, Retire: p.Retire},
	}
}

func (p *Provider) resourceHooks() resources.Hooks {
	return resources.Hooks{
		Postgres:   &resources.ResourceHooks{Plan: p.PlanPostgres, Provision: p.ProvisionPostgres, Remove: p.RemovePostgres},
		Bucket:     &resources.ResourceHooks{Plan: p.PlanBucket, Provision: p.ProvisionBucket, Remove: p.RemoveBucket},
		KV:         &resources.ResourceHooks{Plan: p.PlanKV, Provision: p.ProvisionKV, Remove: p.RemoveKV},
		Topic:      &resources.ResourceHooks{Plan: p.PlanTopic, Provision: p.ProvisionTopic, Remove: p.RemoveTopic},
		Realtime:   &resources.ResourceHooks{Plan: p.PlanRealtime, Provision: p.ProvisionRealtime, Remove: p.RemoveRealtime},
		Containers: &resources.ContainerHooks{Provision: p.ProvisionContainers, Remove: p.RemoveContainers},
		Retention:  &resources.ImageRetentionHooks{Reconcile: p.ReconcileImages, Forget: p.ForgetReleases},
	}
}

func (p *Provider) Bootstrap(kind edge.Kind) (provider.Bootstrap, error) {
	bootstrap := elevating{Bootstrap: checkedHost{Bootstrap: host.NewBootstrap(p.host, Vendor, p.project), checkHost: p.CheckHost}, elevated: p.elevated}
	if kind == edge.None {
		bootstrap.served = p.refuseServingPortsClosedFromOutside
	}
	return bootstrap, nil
}

func (p *Provider) Stacks() provider.Stacks {
	return resources.NewHookStacks(p.keyValues, p.Artifacts(), p.resourceHooks())
}

func (p *Provider) Artifacts() provider.ArtifactStore { return resources.NoArtifacts{} }

func (p *Provider) KeyValues() records.Store { return p.keyValues }

func (p *Provider) Cipher() seal.Cipher { return p.cipher }

func (p *Provider) Credentials() provider.Credentials { return credentials{p} }

func (p *Provider) Edges() provider.Edges { return edges{p} }

func (p *Provider) Routers() provider.Routers { return routers{p} }

func (p *Provider) DNS() provider.DNS { return dns{} }

func (p *Provider) Certificates() provider.Certificates { return certificates{p} }

func (p *Provider) Connector() provider.Connector { return connector{p} }

func (p *Provider) Runtime() provider.Runtime { return containerRuntime{p} }

func (p *Provider) Liveness() provider.Liveness { return &p.Net }

func (p *Provider) Logs() provider.Logs { return logs{p} }
