package deploy

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/functionnetwork"
)

func attachesFunctionNetwork(spec provider.StackSpec, transformed *transformPatches) (functions, workers bool) {
	app := spec.App
	if !functionnetwork.IsNeededForAny(app.Grants) {
		return false, false
	}
	functions = slices.ContainsFunc(app.Functions, func(fn provider.FunctionSpec) bool { return !transformed.placesInVPC(fn.Name) })
	workers = len(app.Workers) > 0 && spec.Ref.Name.Env != ""
	return functions, workers
}

func (r *release) readFunctionNetworkFor(ctx context.Context, spec provider.StackSpec, transformed *transformPatches) (*functionnetwork.Network, bool, error) {
	functions, workers := attachesFunctionNetwork(spec, transformed)
	if !functions && !workers {
		return nil, false, nil
	}
	network, present, err := r.functionNetwork.Read(ctx, spec.Ref.Tier)
	if err != nil {
		return nil, false, err
	}
	if !present {
		network = functionnetwork.Pending
	}
	return &network, !present, nil
}

func (r *release) claimFunctionNetworkFor(ctx context.Context, spec provider.StackSpec, transformed *transformPatches, progress progress.Log) (*functionnetwork.Network, bool, error) {
	functions, workers := attachesFunctionNetwork(spec, transformed)
	if !functions && !workers {
		return nil, false, nil
	}
	claimant := spec.Ref
	if !functions {
		claimant = workersRef(spec.Ref)
	}
	network, err := r.functionNetwork.Claim(ctx, claimant, progress)
	if err != nil {
		return nil, false, err
	}
	return &network, functions, nil
}
