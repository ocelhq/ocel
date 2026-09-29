package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider/liveness"
	"github.com/ocelhq/ocel/pkg/router"
)

func (p *Provider) servedOnTheBox(ctx context.Context, hostname string) (router.Kind, error) {
	said, err := p.host.ProbeRouter(ctx, hostname)
	if err != nil {
		return "", err
	}
	if said.Failure != "" {
		return "", liveness.ProbeUnanswered{Cause: said.Failure}
	}
	return said.Router, nil
}
