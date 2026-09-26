package fake

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
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

func (p *Provider) PreflightDeploy(_ context.Context, pre provider.DeployPreflight) error {
	return p.preflight(pre)
}
