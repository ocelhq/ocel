package aws

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
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
	keyValues records.Store
	region    string

	envSourceSync func(ctx context.Context, tier environment.Tier) (provider.BootstrapPart, bool, error)
	repairParts   func(ctx context.Context, tier environment.Tier) ([]string, error)
	addedParts    func(ctx context.Context, tier environment.Tier) ([]provider.BootstrapPart, error)

	removable []removablePart
}

func (s forgetting) Parts() []provider.Part {
	offered := s.Bootstrap.Parts()
	for _, each := range s.removable {
		offered = append(offered, each.Part)
	}
	return offered
}

func (s forgetting) addedRemovals(req provider.BootstrapRequest) ([]removablePart, provider.BootstrapRequest) {
	var added []removablePart
	var stacks []string
	for _, name := range req.Remove {
		if at := slices.IndexFunc(s.removable, func(each removablePart) bool { return each.Name == name }); at >= 0 {
			added = append(added, s.removable[at])
		} else {
			stacks = append(stacks, name)
		}
	}
	req.Remove = stacks
	return added, req
}

func (s forgetting) Plan(ctx context.Context, req provider.BootstrapRequest) (provider.Plan, error) {
	added, req := s.addedRemovals(req)
	plan, err := s.Bootstrap.Plan(ctx, req)
	if err != nil {
		return provider.Plan{}, err
	}
	for _, each := range added {
		group, err := each.plan(ctx, req.Tier)
		if err != nil {
			return provider.Plan{}, err
		}
		if group != nil {
			plan.Groups = append(plan.Groups, *group)
		}
	}
	return plan, nil
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
	network, present, err := deploy.ReadFunctionNetworkPart(ctx, s.keyValues, s.region, described.Tier)
	if err != nil {
		return nil, err
	}
	if present {
		parts = append(parts, network)
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
	added, req := s.addedRemovals(req)
	if err := s.Bootstrap.Apply(ctx, req, progress); err != nil {
		return err
	}
	s.forget()
	for _, each := range added {
		if err := each.remove(ctx, req.Tier, progress); err != nil {
			return fmt.Errorf("remove the %s tier's %s bootstrap part: %w", req.Tier, each.Name, err)
		}
		if err := stackrecords.ForgetBootstrapPart(ctx, s.keyValues, req.Tier, each.Name); err != nil {
			return err
		}
		if progress != nil {
			progress.Say("Removed the " + each.Name + " part of the " + string(req.Tier) + " bootstrap")
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
