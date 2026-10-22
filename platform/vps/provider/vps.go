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
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/component"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/release"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/bucket"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/container"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/kv"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/postgres"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/realtime"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/topic"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
	"github.com/ocelhq/ocel/platform/vps/provider/tunnel"
)

const Vendor provider.Vendor = "vps"

type Provider struct {
	options    Options
	project    string
	host       *host.Host
	routing    *routing.Table
	tunnel     *tunnel.Container
	pins       *certs.Pins
	containers *release.Containers
	images     *release.Images
	ssh        *ssh.Box
	resources  *container.Containers
	keyValues  records.Store
	cipher     *boxstore.Cipher

	transform transform.Pass
	resolve   Lookup
	reaches   Reach
	now       func() time.Time

	stores bucket.Stores
	queues topic.QueueDatabases

	dial       sync.Mutex
	sshSession *ssh.Session

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
	if err := decoded.Proxy.RefuseUnusable(decoded.Certificates); err != nil {
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
		Preflight:          &provider.PreflightHooks{Plan: p.PlanPreflight, Apply: p.ApplyPreflight},
		OpenRegistryImages: p.OpenRegistryImages,
		OpenDirectImages:   p.OpenDirectImages,
		CheckBucket:        s3store.Check,
		CheckHost:          p.CheckHost,
		BindingAccess:      &provider.BindingAccessHooks{ForwardPorts: p.ForwardPorts, ServeProxy: p.ServeBindingProxy},
		Retirement:         &provider.RetirementHooks{Plan: p.PlanRetirement, Retire: p.Retire},

		ListProjectResources: p.ListProjectResources,
	}
}

func (p *Provider) resourceHooks() resources.Hooks {
	return resources.Hooks{
		Postgres:   component.Hooks(postgres.New(p.components())),
		Bucket:     component.Hooks(p.buckets()),
		KV:         component.Hooks(kv.New(p.components())),
		Topic:      component.Hooks(topic.New(p.components(), &p.queues)),
		Realtime:   component.Hooks(realtime.New(p.components())),
		Containers: &resources.ContainerHooks{Provision: p.ProvisionContainers, Remove: p.RemoveContainers},
		Retention:  &resources.ImageRetentionHooks{Reconcile: p.ReconcileImages, Forget: p.ForgetReleases},

		ListResources: p.ListResources,
	}
}

func (p *Provider) Bootstrap(kind edge.Kind) (provider.Bootstrap, error) {
	bootstrap := elevating{Bootstrap: host.NewBootstrap(p.host, Vendor, p.project), elevated: p.elevated}
	if kind == edge.None {
		bootstrap.served = p.refuseServingPortsClosedFromOutside
		return bootstrap, nil
	}
	front, err := edges{provider: p}.Open(kind, nil)
	if err != nil {
		return nil, err
	}
	bootstrap.front = front
	return bootstrap, nil
}

func (p *Provider) Stacks() provider.Stacks {
	return resources.NewHookStacks(p.keyValues, p.resourceHooks())
}

func (p *Provider) Artifacts() provider.ArtifactStore { return resources.NoArtifacts{} }

func (p *Provider) KeyValues() records.Store { return p.keyValues }

func (p *Provider) Cipher() seal.Cipher { return p.cipher }

func (p *Provider) Credentials() provider.Credentials { return credentials{p} }

func (p *Provider) Edges() provider.Edges { return edges{p} }

func (p *Provider) Routers() provider.Routers { return routers{p} }

func (p *Provider) DNS() provider.DNS { return dns{cloudflare: p.cloudflareOrigin()} }

func (p *Provider) Certificates() provider.Certificates { return certificates{p} }

func (p *Provider) Connector() provider.Connector { return connector{p} }

func (p *Provider) Runtime() provider.Runtime { return containerRuntime{p} }

func (p *Provider) Liveness() provider.Liveness { return &p.Net }

func (p *Provider) Logs() provider.Logs { return logs{p} }

func nodePass(root string, modules []string) transform.Pass {
	return transform.NodePass{
		Root: root, Modules: modules,
		Uninstalled: "Install `@ocel/transforms` as a devDependency",
	}
}
