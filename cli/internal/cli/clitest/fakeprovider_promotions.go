package clitest

import (
	"context"

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
