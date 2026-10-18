package fake

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/provider"
)

func (p *Provider) RefusePreflight(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.preflightRefusal = err
}

func (p *Provider) Preflighted() []provider.DeployPreflight {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.preflighted)
}

func (p *Provider) preflight(pre provider.DeployPreflight) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.preflighted = append(p.preflighted, pre)
	return p.preflightRefusal
}

func (p *Provider) PlanPreflight(_ context.Context, pre provider.DeployPreflight) (provider.Plan, error) {
	return provider.Plan{}, p.preflight(pre)
}

func (p *Provider) ApplyPreflight(_ context.Context, pre provider.DeployPreflight) error {
	return p.preflight(pre)
}
