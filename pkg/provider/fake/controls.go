package fake

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/provider/resources"
)

const KeptPromotions = ledger.KeptPromotions

func (p *Provider) Releases(tier environment.Tier, project string) *ledger.Ledger {
	return ledger.New(p.KeyValues(), tier, project)
}

func (p *Provider) WithHooks(set func(*provider.Hooks)) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	set(&p.hooks)
	return p
}

func (p *Provider) WithFacts(set func(*provider.Facts)) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.setFacts = set
	return p
}

func EnsureBindingAccess(hooks *provider.Hooks) *provider.BindingAccessHooks {
	if hooks.BindingAccess == nil {
		hooks.BindingAccess = &provider.BindingAccessHooks{
			ForwardPorts: func(context.Context, provider.PortForwardRequest, progress.Log) ([]provider.PortForward, error) {
				return nil, nil
			},
			ServeProxy: func(context.Context, provider.BindingProxyRequest, progress.Log) (provider.BindingProxy, error) {
				return provider.BindingProxy{}, nil
			},
		}
	}
	return hooks.BindingAccess
}

func (p *Provider) everyHook(hooks *provider.Hooks) {
	hooks.Preflight = &provider.PreflightHooks{Plan: p.PlanPreflight, Apply: p.ApplyPreflight}
	hooks.EnsureImageRegistry = p.EnsureImageRegistry
	hooks.EnvSource = &provider.EnvSourceHooks{ProveIdentity: p.ProveIdentity, EnsureSync: func(context.Context, environment.Tier, string) error { return nil }}
}

func (p *Provider) ResourceHooks() resources.Hooks {
	return resources.Hooks{
		Postgres:   p.primitiveHooks(),
		Bucket:     p.primitiveHooks(),
		Functions:  &resources.FunctionHooks{Provision: p.ProvisionFunctions, Remove: p.RemoveFunctions},
		Containers: &resources.ContainerHooks{Provision: p.ProvisionContainers, Remove: p.RemoveContainers},
	}
}

func (p *Provider) WithArtifactStore(store provider.ArtifactStore) *Provider {
	p.artifacts = store
	p.stacks.artifacts = store
	return p
}

func (p *Provider) ImageStore() *Images { return p.images }

func (p *Provider) Region() string { return p.options.Region }

func (p *Provider) FakeBootstrap() *Bootstrap { return p.bootstrap }

func (p *Provider) Journal() []string { return p.journal.Entries() }

func (p *Provider) ResourceStacks(hooks resources.Hooks) *Provider {
	p.resourceStacks = resources.NewHookStacks(p.keyValues, hooks)
	return p
}

func (p *Provider) FakeStacks() *Stacks { return p.stacks }

func (p *Provider) FakeConnector() *Connector { return p.connector }

func (p *Provider) FakeLogs() *Logs { return &p.logs }
