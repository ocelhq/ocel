package cli

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// fakePromotions is the canned promotion history deployFakeProviderServer's
// ListPromotions/Rollback serve, newest-first with "promo-2" active — enough
// for TestRunRollback_* / TestRunDeploymentsLs_* to exercise the default
// ("previous promotion") and --to paths without a real deployments store.
var fakePromotions = []*deploymentsv1.PromotionHistoryEntry{
	{Promotion: &deploymentsv1.Promotion{PromotionId: "promo-2", Ts: 2000, Builds: map[string]string{"web": "build-2"}}, Active: true},
	{Promotion: &deploymentsv1.Promotion{PromotionId: "promo-1", Ts: 1000, Builds: map[string]string{"web": "build-1"}}, Active: false},
}

// ListPromotions returns the canned promotion history so `ocel deployments
// ls` has something to render.
func (s *deployFakeProviderServer) ListPromotions(ctx context.Context, req *deploymentsv1.ListPromotionsRequest) (*deploymentsv1.ListPromotionsResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	return &deploymentsv1.ListPromotionsResponse{Promotions: fakePromotions}, nil
}

// Rollback promotes fakePromotions' entry immediately before the active one
// when req.To is empty, or the named entry otherwise; an unknown id is
// rejected, mirroring the real provider's clear-error contract.
func (s *deployFakeProviderServer) Rollback(ctx context.Context, req *deploymentsv1.RollbackRequest) (*deploymentsv1.RollbackResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}

	to := req.GetTo()
	if to == "" {
		to = "promo-1"
	}
	for _, entry := range fakePromotions {
		if entry.GetPromotion().GetPromotionId() == to {
			return &deploymentsv1.RollbackResponse{
				Promoted: &deploymentsv1.Promotion{
					PromotionId: entry.GetPromotion().GetPromotionId(),
					Ts:          9999,
					Builds:      entry.GetPromotion().GetBuilds(),
				},
			}, nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no promotion %q in this project's history", to))
}
