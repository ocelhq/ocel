package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
)

func (p *Provider) forget() {
	p.deployed.forget()
	p.params.forget()
	p.layers.forget()
}

type forgetting struct {
	provider.Bootstrap
	forget    func()
	key       func(context.Context, environment.Tier) (string, error)
	keyValues keyvalue.Store
	region    string
}

func (s forgetting) DescribeParts(ctx context.Context, described provider.BootstrapDescription) ([]provider.BootstrapPart, error) {
	parts, err := s.Bootstrap.DescribeParts(ctx, described)
	if err != nil || !described.Present {
		return parts, err
	}
	containers, present, err := deploy.ReadContainerInfrastructurePart(ctx, s.keyValues, s.region, described.Tier)
	if err != nil || !present {
		return parts, err
	}
	return append(parts, containers), nil
}

func (s forgetting) Apply(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
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
	return envsource.ForgetDigestKey(ctx, s.keyValues, req.Tier)
}

func (s forgetting) Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	if err := s.Bootstrap.Remove(ctx, tier, progress); err != nil {
		return err
	}
	s.forget()
	return nil
}
