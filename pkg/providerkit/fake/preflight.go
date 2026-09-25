package fake

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func (p *Provider) RefusePreflight(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.preflightRefusal = err
}

func (p *Provider) Preflighted() []providerkit.DeployPreflight {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.preflighted)
}

func (p *Provider) preflight(pre providerkit.DeployPreflight) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.preflighted = append(p.preflighted, pre)
	return p.preflightRefusal
}

func (p *Provider) PreflightDeploy(_ context.Context, pre providerkit.DeployPreflight) error {
	return p.preflight(pre)
}
