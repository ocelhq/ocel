package fake

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
)

func (p *Provider) Hook(set func(*providerkit.Hooks)) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	set(&p.hooks)
	return p
}

func (p *Provider) everyHook(hooks *providerkit.Hooks) {
	hooks.WarmFunctions = func(context.Context, []string, providerkit.Progress) error { return nil }
	hooks.EmbedCode = func(context.Context, string, providerkit.ArtifactRef, providerkit.Progress) error { return nil }
	hooks.InspectStack = p.InspectStack
	hooks.VerifyGrants = func(context.Context, providerkit.Binding) error { return nil }
	hooks.PreflightDeploy = p.PreflightDeploy
	hooks.EnsureImageRegistry = p.EnsureImageRegistry
}

func (p *Provider) ResourceHooks() resources.Hooks {
	return resources.Hooks{
		Functions:  &resources.FunctionHooks{Provision: p.ProvisionFunctions, Remove: p.RemoveFunctions},
		Containers: &resources.ContainerHooks{Provision: p.ProvisionContainers, Remove: p.RemoveContainers},
	}
}

func (p *Provider) Ships(store providerkit.ArtifactStore) *Provider {
	p.artifacts = store
	p.releases.artifacts = store
	return p
}

func (p *Provider) Registry() *Images { return p.images }

func (p *Provider) Region() string { return p.options.Region }

func (p *Provider) FakeBootstrap() *Bootstrap { return p.bootstrap }

func (p *Provider) Journal() []string { return p.journal.Entries() }

func (p *Provider) Releasing(hooks resources.Hooks) *Provider {
	p.releasing = resources.Stacks(p.records, p.artifacts, hooks)
	return p
}

func (p *Provider) FakeStacks() *Stacks { return p.releases }
