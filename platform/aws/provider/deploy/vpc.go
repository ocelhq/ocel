package deploy

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"
)

func (r *release) planNetwork(ctx context.Context, tier environment.Tier) ([]provider.ChangeGroup, error) {
	_, present, err := r.network.Read(ctx, tier)
	if err != nil {
		return nil, err
	}
	if !present {
		return []provider.ChangeGroup{network.NewChangeGroup(r.cfg.Namespace, tier)}, nil
	}
	repatched, err := r.network.PlanRepatch(ctx, tier)
	if err != nil || repatched == nil {
		return nil, err
	}
	return []provider.ChangeGroup{*repatched}, nil
}

func (r *release) planRepatches(ctx context.Context, tier environment.Tier, functionNetwork bool) ([]provider.ChangeGroup, error) {
	var groups []provider.ChangeGroup
	repatched, err := r.network.PlanRepatch(ctx, tier)
	if err != nil {
		return nil, err
	}
	if repatched != nil {
		groups = append(groups, *repatched)
	}
	if !functionNetwork {
		return groups, nil
	}
	if repatched, err = r.functionNetwork.PlanRepatch(ctx, tier); err != nil {
		return nil, err
	}
	if repatched != nil {
		groups = append(groups, *repatched)
	}
	return groups, nil
}

func (r *release) placeInVPC(ctx context.Context, spec provider.StackSpec, work *infraWork, progress progress.Log) error {
	if len(r.registry.PlacesInVPC(spec.Resources)) == 0 {
		return nil
	}
	if !work.previewing {
		placed, err := r.network.Claim(ctx, spec.Ref, progress)
		if err != nil {
			return err
		}
		work.network = &placed
		return nil
	}
	placed, present, err := r.network.Read(ctx, spec.Ref.Tier)
	if err != nil {
		return err
	}
	if !present {
		placed = network.Pending
		work.networkPending = true
	}
	work.network = &placed
	return nil
}
