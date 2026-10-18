package aws

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
)

func (p *Provider) forget() {
	p.deployed.forget()
	p.params.forget()
	p.layers.forget()
	p.partReads.Forget()
}

type tierBootstrap struct {
	provider.Bootstrap
	forget    func()
	key       func(context.Context, environment.Tier) (string, error)
	keyValues records.Store
	parts     tier.Parts
}

func (s tierBootstrap) Parts() []provider.Part { return s.parts.Offered() }

func (s tierBootstrap) removals(req provider.BootstrapRequest) (tier.Parts, provider.BootstrapRequest) {
	var removed tier.Parts
	var stacks []string
	for _, name := range req.Remove {
		if part, found := s.parts.Named(name); found && part.Removal != nil {
			removed = append(removed, part)
		} else {
			stacks = append(stacks, name)
		}
	}
	req.Remove = stacks
	return removed, req
}

func (s tierBootstrap) Plan(ctx context.Context, req provider.BootstrapRequest) (provider.Plan, error) {
	removed, req := s.removals(req)
	plan, err := s.Bootstrap.Plan(ctx, req)
	if err != nil {
		return provider.Plan{}, err
	}
	for _, part := range removed {
		group, err := part.Removal.Plan(ctx, req.Tier)
		if err != nil {
			return provider.Plan{}, err
		}
		if group != nil {
			plan.Groups = append(plan.Groups, *group)
		}
	}
	return plan, nil
}

func (s tierBootstrap) DescribeParts(ctx context.Context, described provider.BootstrapDescription) ([]provider.BootstrapPart, error) {
	parts, err := s.Bootstrap.DescribeParts(ctx, described)
	if err != nil || !described.Present {
		return parts, err
	}
	for _, part := range s.parts {
		if part.Describe == nil {
			continue
		}
		each, present, err := part.Describe(ctx, described.Tier)
		if err != nil {
			return nil, err
		}
		if present {
			parts = append(parts, each)
		}
	}
	return parts, nil
}

func (s tierBootstrap) Apply(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	sealing, err := s.key(ctx, req.Tier)
	if err != nil {
		return err
	}
	removed, req := s.removals(req)
	if err := s.Bootstrap.Apply(ctx, req, progress); err != nil {
		return err
	}
	s.forget()
	for _, part := range removed {
		if err := part.Removal.Remove(ctx, req.Tier, progress); err != nil {
			return fmt.Errorf("remove the %s tier's %s bootstrap part: %w", req.Tier, part.Name, err)
		}
		if err := stackrecords.ForgetBootstrapPart(ctx, s.keyValues, req.Tier, part.Name); err != nil {
			return err
		}
		if progress != nil {
			progress.Say("Removed the " + part.Name + " part of the " + string(req.Tier) + " bootstrap")
		}
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

func (s tierBootstrap) repair(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	var repaired []string
	for _, part := range s.parts {
		if part.Repair == nil {
			continue
		}
		made, err := part.Repair(ctx, tier)
		if err != nil {
			return err
		}
		if made {
			repaired = append(repaired, part.Name)
		}
	}
	if progress == nil {
		return nil
	}
	for _, name := range repaired {
		progress.Say("Made the " + name + " part again from this build, mending any drift")
	}
	return nil
}

func (s tierBootstrap) Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	if err := s.Bootstrap.Remove(ctx, tier, progress); err != nil {
		return err
	}
	s.forget()
	return nil
}
