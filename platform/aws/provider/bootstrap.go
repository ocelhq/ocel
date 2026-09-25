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
	providerkit.Bootstrapper
	settled func()
}

func (s settling) Apply(ctx context.Context, req providerkit.BootstrapRequest, report providerkit.Reporter) error {
	if err := s.Bootstrapper.Apply(ctx, req, report); err != nil {
		return err
	}
	s.settled()
	return nil
}

func (s settling) Remove(ctx context.Context, class providerkit.Class, report providerkit.Reporter) error {
	if err := s.Bootstrapper.Remove(ctx, class, report); err != nil {
		return err
	}
	s.settled()
	return nil
}
