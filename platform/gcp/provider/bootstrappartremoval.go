package gcp

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

type removablePart struct {
	provider.Part
	deployed func(bootstrap, environment.Tier) part
	plan     func(bootstrap, context.Context, environment.Tier) ([]provider.Change, error)
	remove   func(bootstrap, context.Context, environment.Tier, progress.Log) error
}

var removableParts = []removablePart{
	{Part: provider.Part{Name: networkPart, Summary: networkSummary}, deployed: bootstrap.networkPart},
	{Part: provider.Part{Name: tasksPart, Summary: tasksSummary}, deployed: bootstrap.tasksPart},
	{Part: provider.Part{Name: tagDatabasePart, Summary: tagDatabaseSummary}, deployed: bootstrap.tagDatabasePart},
	{Part: provider.Part{Name: albPart, Summary: albSummary}, deployed: bootstrap.albPart},
	{Part: provider.Part{Name: envSourceSyncPart, Summary: envSourceSyncSummary}, plan: bootstrap.envSourceSyncRemovals, remove: bootstrap.removeEnvSourceSyncPart},
	{Part: provider.Part{Name: bastionPart, Summary: bastionSummary}, plan: bootstrap.bastionRemovals, remove: bootstrap.removeBastion},
}

type partRemoval struct {
	force bool
	retry string
}

func namedRemoval(tier environment.Tier, name string, force bool) partRemoval {
	return partRemoval{force: force, retry: provider.BootstrapCommand(tier) + " --remove " + name}
}

func (bootstrap) Parts() []provider.Part {
	offered := credentialParts()
	for _, each := range removableParts {
		offered = append(offered, each.Part)
	}
	return offered
}

func splitRemovals(req provider.BootstrapRequest) ([]string, provider.BootstrapRequest) {
	var named, rest []string
	for _, name := range req.Remove {
		if slices.ContainsFunc(removableParts, func(each removablePart) bool { return each.Name == name }) {
			named = append(named, name)
		} else {
			rest = append(rest, name)
		}
	}
	req.Remove = rest
	return named, req
}

type plannedRemoval struct {
	removable removablePart
	deployed  part
	group     provider.ChangeGroup
}

func (b bootstrap) planNamedRemovals(ctx context.Context, tier environment.Tier, names []string, force bool) ([]plannedRemoval, error) {
	var planned []plannedRemoval
	for _, each := range removableParts {
		if !slices.Contains(names, each.Name) {
			continue
		}
		removal, present, err := b.planNamedRemoval(ctx, tier, each, force)
		if err != nil {
			return nil, err
		}
		if present {
			planned = append(planned, removal)
		}
	}
	return planned, nil
}

func (b bootstrap) planNamedRemoval(ctx context.Context, tier environment.Tier, each removablePart, force bool) (plannedRemoval, bool, error) {
	if each.deployed == nil {
		changes, err := each.plan(b, ctx, tier)
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
	deployed := each.deployed(b, tier)
	_, present, err := stackrecords.ReadBootstrapPart(ctx, b.records, tier, each.Name)
	if err != nil || !present {
		return plannedRemoval{}, false, err
	}
	if deployed.refuseInUse != nil {
		if err := deployed.refuseInUse(ctx, namedRemoval(tier, each.Name, force)); err != nil {
			return plannedRemoval{}, false, err
		}
	}
	return plannedRemoval{removable: each, deployed: deployed, group: b.removalGroup(tier, deployed)}, true, nil
}

func (b bootstrap) planRemovedParts(ctx context.Context, tier environment.Tier, names []string, force bool) ([]provider.ChangeGroup, error) {
	planned, err := b.planNamedRemovals(ctx, tier, names, force)
	if err != nil {
		return nil, err
	}
	groups := make([]provider.ChangeGroup, 0, len(planned))
	for _, each := range planned {
		groups = append(groups, each.group)
	}
	return groups, nil
}

func (b bootstrap) removeNamedParts(ctx context.Context, tier environment.Tier, names []string, force bool, progress progress.Log) error {
	planned, err := b.planNamedRemovals(ctx, tier, names, force)
	if err != nil {
		return err
	}
	for _, each := range slices.Backward(planned) {
		if each.removable.deployed != nil {
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

func (b bootstrap) removeUnrecordedPart(ctx context.Context, tier environment.Tier, each removablePart, progress progress.Log) error {
	if err := each.remove(b, ctx, tier, progress); err != nil {
		return fmt.Errorf("remove the %s part of the %s bootstrap: %w", each.Name, tier, err)
	}
	gcloud.EnsureProgress(progress).Say("Removed the " + each.Name + " part of the " + string(tier) + " bootstrap")
	return nil
}

func (b bootstrap) removeEnvSourceSyncPart(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	if err := b.removeEnvSourceSync(ctx, tier, progress); err != nil {
		return err
	}
	return stackrecords.ForgetBootstrapPart(ctx, b.records, tier, envSourceSyncPart)
}

func (b bootstrap) removePart(ctx context.Context, tier environment.Tier, each part, progress progress.Log) error {
	if err := each.remove(ctx); err != nil {
		return fmt.Errorf("remove the %s part of the %s bootstrap: %w", each.name, tier, err)
	}
	if err := stackrecords.ForgetBootstrapPart(ctx, b.records, tier, each.name); err != nil {
		return err
	}
	gcloud.EnsureProgress(progress).Say("Removed the " + each.name + " part of the " + string(tier) + " bootstrap")
	return nil
}
