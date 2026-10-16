package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
)

func (p *Provider) forget() {
	p.deployed.forget()
	p.params.forget()
	p.layers.forget()
	p.parts.forget()
}

type forgetting struct {
	provider.Bootstrap
	forget    func()
	key       func(context.Context, environment.Tier) (string, error)
	keyValues keyvalue.Store
	region    string

	envSourceSync func(ctx context.Context, tier environment.Tier) (provider.BootstrapPart, bool, error)
	repairParts   func(ctx context.Context, tier environment.Tier) ([]string, error)
	addedParts    func(ctx context.Context, tier environment.Tier) ([]provider.BootstrapPart, error)
}

func (s forgetting) DescribeParts(ctx context.Context, described provider.BootstrapDescription) ([]provider.BootstrapPart, error) {
	parts, err := s.Bootstrap.DescribeParts(ctx, described)
	if err != nil || !described.Present {
		return parts, err
	}
	containers, present, err := deploy.ReadContainerInfrastructurePart(ctx, s.keyValues, s.region, described.Tier)
	if err != nil {
		return nil, err
	}
	if present {
		parts = append(parts, containers)
	}
	sync, present, err := s.envSourceSync(ctx, described.Tier)
	if err != nil {
		return nil, err
	}
	if present {
		parts = append(parts, sync)
	}
	added, err := s.addedParts(ctx, described.Tier)
	if err != nil {
		return nil, err
	}
	return append(parts, added...), nil
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
	if err := s.repair(ctx, req.Tier, progress); err != nil {
		return err
	}
	if sealing == "" {
		return nil
	}
	now, err := s.key(ctx, req.Tier)
	if err != nil || now == sealing {
		return err
	}
	return envsource.ForgetDigestKey(ctx, s.keyValues, req.Tier)
}

func (s forgetting) repair(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	if s.repairParts == nil {
		return nil
	}
	repaired, err := s.repairParts(ctx, tier)
	if err != nil || progress == nil {
		return err
	}
	for _, part := range repaired {
		progress.Say("Made the " + part + " part again from this build, mending any drift")
	}
	return nil
}

func (p *Provider) repairParts(ctx context.Context, tier environment.Tier) ([]string, error) {
	var works []stackrecords.PartWork
	sync, keyed, err := p.envSourceSyncWork(ctx, tier, "", true)
	if err != nil {
		return nil, err
	}
	if keyed {
		works = append(works, sync)
	}
	added, err := p.addedPartWorks(ctx, tier)
	if err != nil {
		return nil, err
	}
	return stackrecords.RepairBootstrapParts(ctx, p.KeyValues(), tier, append(works, added...))
}

func (s forgetting) Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	if err := s.Bootstrap.Remove(ctx, tier, progress); err != nil {
		return err
	}
	s.forget()
	return nil
}
