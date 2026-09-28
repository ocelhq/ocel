package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
)

func (p *Provider) forget() {
	p.deployed.forget()
	p.params.forget()
}

type forgetting struct {
	provider.Bootstrap
	forget  func()
	key     func(context.Context, environment.Tier) (string, error)
	records records.Store
}

func (s forgetting) Apply(ctx context.Context, req provider.BootstrapRequest, progress progress.Progress) error {
	sealing, err := s.key(ctx, req.Tier)
	if err != nil {
		return err
	}
	if err := s.Bootstrap.Apply(ctx, req, progress); err != nil {
		return err
	}
	s.forget()
	if sealing == "" {
		return nil
	}
	now, err := s.key(ctx, req.Tier)
	if err != nil || now == sealing {
		return err
	}
	return envsource.ForgetDigestKey(ctx, s.records, req.Tier)
}

func (s forgetting) Remove(ctx context.Context, tier environment.Tier, progress progress.Progress) error {
	if err := s.Bootstrap.Remove(ctx, tier, progress); err != nil {
		return err
	}
	s.forget()
	return nil
}
