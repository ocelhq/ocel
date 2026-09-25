package vps

import (
	"context"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	"github.com/ocelhq/ocel/pkg/transformkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const Vendor providerkit.Vendor = "vps"

type Provider struct {
	options Options
	project string
	host    *host.Host
	records providerkit.RecordStore
	sealer  *host.Cipher

	transform transformkit.Pass
	resolve   Lookup
	reaches   Reach

	stores standingStores

	dial sync.Mutex
	live *session.Session

	providerkit.NetLiveness
}

func New(_ context.Context, settings providerkit.Settings) (providerkit.Provider, error) {
	decoded, err := providerkit.Decode[Options](Vendor, settings.Options)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(decoded.SSH.Alias) == "" && strings.TrimSpace(decoded.SSH.Host) == "" {
		return nil, providerkit.Refuse(providerkit.CodeInvalid, "option %q names no machine: give it an ssh_config alias or an object with a host", "ssh")
	}
	if port := decoded.SSH.Port; port != 0 && (port < 1 || port > 65535) {
		return nil, providerkit.Refuse(providerkit.CodeInvalid, "option %q names port %d, which is outside 1-65535", "ssh", port)
	}
	if err := decoded.Proxy.usable(decoded.Certificates); err != nil {
		return nil, err
	}
	p := NewProvider(decoded)
	p.project = settings.Slug
	if len(settings.Transforms) > 0 {
		p.transform = nodePass(settings.Transforms)
	}
	return p, nil
}

func NewProvider(options Options) *Provider {
	p := &Provider{options: options}
	return p.wire(p.conn)
}

func newProvider(options Options, dial host.Dial) *Provider {
	return (&Provider{options: options}).wire(dial)
}

func (p *Provider) Facts() providerkit.Facts {
	return providerkit.Facts{
		Vendor:            Vendor,
		Bindings:          resources.Serves(p.resourceHooks()),
		Computes:          []providerkit.Compute{providerkit.ComputeContainer},
		Edges:             []edge.Kind{box.Kind},
		DefaultEdge:       box.Kind,
		DNSKinds:          []providerkit.DNSKind{dnsCloudflare},
		RendersTransforms: true,
	}
}

func (p *Provider) Hooks() providerkit.Hooks {
	return providerkit.Hooks{
		PreflightDeploy:    p.PreflightDeploy,
		OpenRegistryImages: p.OpenRegistryImages,
		OpenDirectImages:   p.OpenDirectImages,
		CheckHost:          p.CheckHost,
	}
}

func (p *Provider) resourceHooks() resources.Hooks {
	return resources.Hooks{
		ProvisionPostgres: p.ProvisionPostgres,
		ProvisionBucket:   p.ProvisionBucket,
		RemoveResource:    p.RemoveResource,
		Containers:        &resources.ContainerHooks{Provision: p.ProvisionContainers, Remove: p.RemoveContainers},
		Retention:         &resources.RetentionHooks{Reconcile: p.ReconcileImages, Forget: p.ForgetReleases},
	}
}

func (p *Provider) Bootstrap(edge.Kind) (providerkit.Bootstrap, error) {
	return elevating{Bootstrap: host.NewBootstrap(p.host, Vendor, p.project), elevated: p.elevated}, nil
}

func (p *Provider) Stacks() providerkit.Stacks {
	return resources.Stacks(p.records, p.Artifacts(), p.resourceHooks())
}

func (p *Provider) Artifacts() providerkit.ArtifactStore { return providerkit.NoArtifacts{} }

func (p *Provider) Records() providerkit.RecordStore { return p.records }

func (p *Provider) Cipher() providerkit.Cipher { return p.sealer }

func (p *Provider) Credentials() providerkit.Credentials { return credentials{p} }

func (p *Provider) Edges() providerkit.Edges { return edges{p} }

func (p *Provider) DNS() providerkit.DNS { return dns{} }

func (p *Provider) Certificates() providerkit.Certificates { return certificates{p} }

func (p *Provider) Connector() providerkit.Connector { return connector{p} }

func (p *Provider) Runtime() providerkit.Runtime { return containerRuntime{p} }

func (p *Provider) Liveness() providerkit.Liveness { return &p.NetLiveness }
