package bootstrap

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

type RemovablePart struct {
	provider.Part
	Deployed func(Engine, environment.Tier) Part
	Plan     func(context.Context, Engine, environment.Tier) ([]provider.Change, error)
	Remove   func(context.Context, Engine, environment.Tier, progress.Log) error
	Take     func(context.Context, Engine, environment.Tier, progress.Log) error
	Describe func(context.Context, Engine, environment.Tier) (provider.BootstrapPart, bool, error)
	Work     func(Engine, environment.Tier) stackrecords.PartWork
}

type PartRemoval struct {
	Force  bool
	Retry  string
	Fronts []string
}

func namedRemoval(tier environment.Tier, name string, force bool, fronts []string) PartRemoval {
	return PartRemoval{Force: force, Retry: provider.BootstrapCommand(tier) + " --remove " + name, Fronts: fronts}
}

func (b Engine) Parts() []provider.Part {
	offered := CredentialParts()
	for _, each := range b.Removable {
		offered = append(offered, each.Part)
	}
	return offered
}

func (b Engine) SplitRemovals(req provider.BootstrapRequest) ([]string, provider.BootstrapRequest) {
	var named, rest []string
	for _, name := range req.Remove {
		if slices.ContainsFunc(b.Removable, func(each RemovablePart) bool { return each.Name == name }) {
			named = append(named, name)
		} else {
			rest = append(rest, name)
		}
	}
	req.Remove = rest
	return named, req
}

type plannedRemoval struct {
	removable RemovablePart
	deployed  Part
	group     provider.ChangeGroup
}

func (b Engine) planNamedRemovals(ctx context.Context, tier environment.Tier, names []string, force bool, fronts []string) ([]plannedRemoval, error) {
	var planned []plannedRemoval
	for _, each := range b.Removable {
		if !slices.Contains(names, each.Name) {
			continue
		}
		removal, present, err := b.planNamedRemoval(ctx, tier, each, force, fronts)
		if err != nil {
			return nil, err
		}
		if present {
			planned = append(planned, removal)
		}
	}
	return planned, nil
}

func (b Engine) planNamedRemoval(ctx context.Context, tier environment.Tier, each RemovablePart, force bool, fronts []string) (plannedRemoval, bool, error) {
	if each.Deployed == nil {
		changes, err := each.Plan(ctx, b, tier)
		if err != nil || len(changes) == 0 {
			return plannedRemoval{}, false, err
		}
		return plannedRemoval{removable: each, group: provider.ChangeGroup{
			Kind:    provider.StackGroupKind,
			Name:    b.partGroupName(tier, each.Name),
			Part:    each.Name,
			Action:  provider.ActionDelete,
			Changes: changes,
		}}, true, nil
	}
	deployed := each.Deployed(b, tier)
	_, present, err := stackrecords.ReadBootstrapPart(ctx, b.Records, tier, each.Name)
	if err != nil || !present {
		return plannedRemoval{}, false, err
	}
	if deployed.RefuseInUse != nil {
		if err := deployed.RefuseInUse(ctx, namedRemoval(tier, each.Name, force, fronts)); err != nil {
			return plannedRemoval{}, false, err
		}
	}
	return plannedRemoval{removable: each, deployed: deployed, group: b.removalGroup(tier, deployed)}, true, nil
}

func (b Engine) PlanRemovedParts(ctx context.Context, tier environment.Tier, names []string, force bool, fronts []string) ([]provider.ChangeGroup, error) {
	planned, err := b.planNamedRemovals(ctx, tier, names, force, fronts)
	if err != nil {
		return nil, err
	}
	groups := make([]provider.ChangeGroup, 0, len(planned))
	for _, each := range planned {
		groups = append(groups, each.group)
	}
	return groups, nil
}

func (b Engine) RemoveNamedParts(ctx context.Context, tier environment.Tier, names []string, force bool, fronts []string, progress progress.Log) error {
	planned, err := b.planNamedRemovals(ctx, tier, names, force, fronts)
	if err != nil {
		return err
	}
	for _, each := range slices.Backward(planned) {
		if each.removable.Deployed != nil {
			err = b.removePart(ctx, tier, each.deployed, progress)
		} else {
			err = b.removeUnrecordedPart(ctx, tier, each.removable, progress)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (b Engine) removeUnrecordedPart(ctx context.Context, tier environment.Tier, each RemovablePart, progress progress.Log) error {
	if err := each.Remove(ctx, b, tier, progress); err != nil {
		return fmt.Errorf("remove the %s part of the %s bootstrap: %w", each.Name, tier, err)
	}
	gcloud.EnsureProgress(progress).Say("Removed the " + each.Name + " part of the " + string(tier) + " bootstrap")
	return nil
}

func (b Engine) removePart(ctx context.Context, tier environment.Tier, each Part, progress progress.Log) error {
	if err := each.Remove(ctx); err != nil {
		return fmt.Errorf("remove the %s part of the %s bootstrap: %w", each.Name, tier, err)
	}
	if err := stackrecords.ForgetBootstrapPart(ctx, b.Records, tier, each.Name); err != nil {
		return err
	}
	gcloud.EnsureProgress(progress).Say("Removed the " + each.Name + " part of the " + string(tier) + " bootstrap")
	return nil
}
