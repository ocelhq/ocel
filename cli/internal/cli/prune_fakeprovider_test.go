package cli

import (
	"context"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// Prune returns a canned reclaim result so `ocel deployments prune` has
// something to render, recording the requested keepN for
// TestRunDeploymentsPrune_* to assert against.
func (s *deployFakeProviderServer) Prune(ctx context.Context, req *deploymentsv1.PruneRequest) (*deploymentsv1.PruneResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	if req.GetKeepN() == 0 {
		return &deploymentsv1.PruneResponse{}, nil
	}
	return &deploymentsv1.PruneResponse{
		KeptPromotionIds:    []string{"promo-2"},
		RemovedPromotionIds: []string{"promo-1"},
	}, nil
}
