package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) forget() {
	p.deployed.forget()
	p.params.forget()
}

type forgetting struct {
	provider.Bootstrap
	forget func()
}

func (s forgetting) Apply(ctx context.Context, req provider.BootstrapRequest, progress edge.Progress) error {
	if err := s.Bootstrap.Apply(ctx, req, progress); err != nil {
		return err
	}
	s.forget()
	return nil
}

func (s forgetting) Remove(ctx context.Context, class edge.Class, progress edge.Progress) error {
	if err := s.Bootstrap.Remove(ctx, class, progress); err != nil {
		return err
	}
	s.forget()
	return nil
}
