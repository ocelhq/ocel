package vps

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func (p *Provider) PreflightDeploy(ctx context.Context, pre provider.DeployPreflight) error {
	if err := p.host.CheckEngine(ctx); err != nil {
		return err
	}
	if err := p.host.FrontAgrees(ctx); err != nil {
		return err
	}
	return refusing([]error{
		p.host.CheckDisk(ctx, repositories(pre.Deploy)),
		p.host.CheckProxy(ctx),
		p.host.ProxyOwnsServingPorts(ctx),
	})
}

func repositories(spec provider.DeploySpec) []string {
	var named []string
	for _, app := range spec.Apps {
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
		var refused refusal.Refusal
		if !errors.As(err, &refused) {
			return err
		}
		said = append(said, refused.Message)
	}
	switch len(said) {
	case 0:
		return nil
	case 1:
		return refusal.Refuse(refusal.CodeNotReady, "%s", said[0])
	default:
		return refusal.Refuse(refusal.CodeNotReady,
			"this box is not ready for a deploy:\n\n%s", strings.Join(said, "\n\n"))
	}
}
