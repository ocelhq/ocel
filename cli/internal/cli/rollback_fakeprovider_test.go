package cli

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

var fakePromotions = []*deploymentsv1.PromotionHistoryEntry{
	{Promotion: &deploymentsv1.Promotion{PromotionId: "promo-2", Ts: 2000, Builds: map[string]string{"web": "build-2~fp2", "admin": "build-2"}}, Active: true},
	{Promotion: &deploymentsv1.Promotion{PromotionId: "promo-1", Ts: 1000, Tag: "v1.0.0", Builds: map[string]string{"web": "build-1"}}, Active: false},
}

func (s *deployFakeProviderServer) ListPromotions(ctx context.Context, req *deploymentsv1.ListPromotionsRequest) (*deploymentsv1.ListPromotionsResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	return &deploymentsv1.ListPromotionsResponse{Promotions: fakePromotions}, nil
}

func (s *deployFakeProviderServer) Rollback(ctx context.Context, req *deploymentsv1.RollbackRequest) (*deploymentsv1.RollbackResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}

	if tag := req.GetTag(); tag != "" {
		for _, entry := range fakePromotions {
			if entry.GetPromotion().GetTag() == tag {
				return rollbackResponseFor(entry), nil
			}
		}
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no promotion tagged %q in this project's history", tag))
	}

	to := req.GetTo()
	if to == "" {
		to = "promo-1"
	}
	for _, entry := range fakePromotions {
		if entry.GetPromotion().GetPromotionId() == to {
			return rollbackResponseFor(entry), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no promotion %q in this project's history", to))
}

func rollbackResponseFor(entry *deploymentsv1.PromotionHistoryEntry) *deploymentsv1.RollbackResponse {
	p := entry.GetPromotion()
	return &deploymentsv1.RollbackResponse{
		Promoted: &deploymentsv1.Promotion{
			PromotionId: p.GetPromotionId(),
			Ts:          9999,
			Builds:      p.GetBuilds(),
			Tag:         p.GetTag(),
		},
	}
}
