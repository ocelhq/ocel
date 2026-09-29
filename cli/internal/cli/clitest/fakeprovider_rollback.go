package clitest

import (
	"context"
	"fmt"
	"os"

	"connectrpc.com/connect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func (s *deployFakeProviderServer) Rollback(ctx context.Context, req *contractv1.RollbackRequest) (*contractv1.RollbackResponse, error) {
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

const FakeRollbackPromotionID = "promo-rollback"

func rollbackResponseFor(entry *contractv1.PromotionHistoryEntry) *contractv1.RollbackResponse {
	rolled := &contractv1.RollbackResponse{
		Promoted: &contractv1.Promotion{
			PromotionId: FakeRollbackPromotionID,
			Ts:          9999,
			Builds:      entry.GetPromotion().GetBuilds(),
			FlipBound:   fakeFlipBound(),
		},
	}
	if warning := os.Getenv(FakeRollbackWarningEnvVar); warning != "" {
		rolled.Warnings = []string{warning}
	}
	return rolled
}
