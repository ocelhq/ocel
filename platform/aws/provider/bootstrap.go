package provider

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func (p *Provider) forget() {
	p.deployed.forget()
	p.params.forget()
}

type settling struct {
	providerkit.Bootstrap
	settled func()
}

func (s settling) Apply(ctx context.Context, req providerkit.BootstrapRequest, progress providerkit.Progress) error {
	if err := s.Bootstrap.Apply(ctx, req, progress); err != nil {
		return err
	}
	s.settled()
	return nil
}

func (s settling) Remove(ctx context.Context, class providerkit.Class, progress providerkit.Progress) error {
	if err := s.Bootstrap.Remove(ctx, class, progress); err != nil {
		return err
	}
	s.settled()
	return nil
}
