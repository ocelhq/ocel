package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) forget() {
	p.deployed.forget()
	p.params.forget()
}

type forgetting struct {
	provider.Bootstrap
	forget  func()
	key     func(context.Context, edge.Class) (string, error)
	records records.Store
}

func (s forgetting) Apply(ctx context.Context, req provider.BootstrapRequest, progress edge.Progress) error {
	sealing, err := s.key(ctx, req.Class)
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
	now, err := s.key(ctx, req.Class)
	if err != nil || now == sealing {
		return err
	}
	return envsource.ForgetDigestKey(ctx, s.records, req.Class)
}

func (s forgetting) Remove(ctx context.Context, class edge.Class, progress edge.Progress) error {
	if err := s.Bootstrap.Remove(ctx, class, progress); err != nil {
		return err
	}
	s.forget()
	return nil
}
