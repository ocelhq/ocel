package fake

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) Hook(set func(*provider.Hooks)) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	set(&p.hooks)
	return p
}

func (p *Provider) everyHook(hooks *provider.Hooks) {
	hooks.WarmFunctions = func(context.Context, []string, edge.Progress) error { return nil }
	hooks.EmbedCode = func(context.Context, string, provider.ArtifactRef, edge.Progress) error { return nil }
	hooks.InspectStack = p.InspectStack
	hooks.VerifyGrants = func(context.Context, provider.Binding) error { return nil }
	hooks.PreflightDeploy = p.PreflightDeploy
	hooks.EnsureImageRegistry = p.EnsureImageRegistry
}

func (p *Provider) ResourceHooks() resources.Hooks {
	return resources.Hooks{
		Functions:  &resources.FunctionHooks{Provision: p.ProvisionFunctions, Remove: p.RemoveFunctions},
		Containers: &resources.ContainerHooks{Provision: p.ProvisionContainers, Remove: p.RemoveContainers},
	}
}

func (p *Provider) Ships(store provider.ArtifactStore) *Provider {
	p.artifacts = store
	p.stacks.artifacts = store
	return p
}

func (p *Provider) Registry() *Images { return p.images }

func (p *Provider) Region() string { return p.options.Region }

func (p *Provider) FakeBootstrap() *Bootstrap { return p.bootstrap }

func (p *Provider) Journal() []string { return p.journal.Entries() }

func (p *Provider) ResourceStacks(hooks resources.Hooks) *Provider {
	p.resourceStacks = resources.Stacks(p.records, p.artifacts, hooks)
	return p
}

func (p *Provider) FakeStacks() *Stacks { return p.stacks }
