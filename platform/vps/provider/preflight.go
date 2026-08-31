package vps

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func (p *Provider) PreflightDeploy(ctx context.Context, pre providerkit.DeployPreflight) error {
	if err := p.host.EngineStanding(ctx); err != nil {
		return err
	}
	return refusing([]error{
		p.host.DiskStanding(ctx, repositories(pre.Plan)),
		p.host.ProxyStanding(ctx),
		p.host.ServingPortsHeld(ctx),
	})
}

func repositories(plan providerkit.DeployPlan) []string {
	var named []string
	for _, app := range plan.Apps {
		repository, ok := host.Repository(app.Image)
		if !ok || slices.Contains(named, repository) {
			continue
		}
		named = append(named, repository)
	}
	return named
}

func refusing(found []error) error {
	var said []string
	for _, err := range found {
		if err == nil {
			continue
		}
		var refusal providerkit.Refusal
		if !errors.As(err, &refusal) {
			return err
		}
		said = append(said, refusal.Message)
	}
	switch len(said) {
	case 0:
		return nil
	case 1:
		return providerkit.Refuse(providerkit.CodeNotReady, "%s", said[0])
	default:
		return providerkit.Refuse(providerkit.CodeNotReady,
			"this box is not ready for a deploy, and nothing was built, transferred or flipped:\n\n%s", strings.Join(said, "\n\n"))
	}
}

var _ providerkit.DeployPreflighter = (*Provider)(nil)
