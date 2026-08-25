package clitest

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

var fakePromotions = []*contractv1.PromotionHistoryEntry{
	{Promotion: &contractv1.Promotion{PromotionId: "promo-2", Ts: 2000, Builds: map[string]string{"web": "build-2~fp2", "admin": "build-2"}}, Active: true},
	{Promotion: &contractv1.Promotion{PromotionId: "promo-1", Ts: 1000, Tag: "v1.0.0", Builds: map[string]string{"web": "build-1"}}, Active: false},
}

func (s *deployFakeProviderServer) ListPromotions(ctx context.Context, req *contractv1.ListPromotionsRequest) (*contractv1.ListPromotionsResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	history := make([]*contractv1.PromotionHistoryEntry, 0, len(fakePromotions))
	for _, entry := range fakePromotions {
		p := proto.Clone(entry.GetPromotion()).(*contractv1.Promotion)
		p.FlipBound = fakeFlipBound()
		history = append(history, &contractv1.PromotionHistoryEntry{Promotion: p, Active: entry.GetActive()})
	}
	return &contractv1.ListPromotionsResponse{Promotions: history}, nil
}

func (s *deployFakeProviderServer) Rollback(ctx context.Context, req *contractv1.RollbackRequest) (*contractv1.RollbackResponse, error) {
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

func rollbackResponseFor(entry *contractv1.PromotionHistoryEntry) *contractv1.RollbackResponse {
	p := entry.GetPromotion()
	return &contractv1.RollbackResponse{
		Promoted: &contractv1.Promotion{
			PromotionId: p.GetPromotionId(),
			Ts:          9999,
			Builds:      p.GetBuilds(),
			Tag:         p.GetTag(),
			FlipBound:   fakeFlipBound(),
		},
	}
}
