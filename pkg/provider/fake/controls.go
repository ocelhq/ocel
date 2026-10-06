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

func (p *Provider) WithProjectDir(dir string) *Provider {
	p.stacks.projectDir = dir
	return p
}

func (p *Provider) WithFacts(set func(*provider.Facts)) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.setFacts = set
	return p
}

func (p *Provider) everyHook(hooks *provider.Hooks) {
	hooks.WarmFunctions = func(context.Context, []string, progress.Log) error { return nil }
	hooks.EmbedCode = func(context.Context, string, provider.ArtifactRef, progress.Log) error { return nil }
	hooks.InspectStack = p.InspectStack
	hooks.VerifyGrants = func(context.Context, provider.Binding) error { return nil }
	hooks.PreflightDeploy = p.PreflightDeploy
	hooks.EnsureImageRegistry = p.EnsureImageRegistry
	hooks.ProveIdentity = p.ProveIdentity
	hooks.ReadNextServerRuntime = p.ReadNextServerRuntime
}

func (p *Provider) ResourceHooks() resources.Hooks {
	return resources.Hooks{
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
	p.resourceStacks = resources.NewHookStacks(p.keyValues, p.artifacts, hooks)
	return p
}

func (p *Provider) FakeStacks() *Stacks { return p.stacks }

func (p *Provider) FakeConnector() *Connector { return p.connector }

func (p *Provider) FakeLogs() *Logs { return &p.logs }
