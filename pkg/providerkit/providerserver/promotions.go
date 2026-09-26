package providerserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (h *handlers) ListPromotions(ctx context.Context, req *contractv1.ListPromotionsRequest) (*contractv1.ListPromotionsResponse, error) {
	session, err := h.openEdgeSession(ctx, edge.ClassProduction, req.GetSlug(), req.GetEdge())
	if undeployed(err) {
		return &contractv1.ListPromotionsResponse{}, nil
	}
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	history, err := session.stack.Ledger().History(ctx, "")
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	return &contractv1.ListPromotionsResponse{Promotions: promotionHistoryProto(history)}, nil
}

func (h *handlers) Rollback(ctx context.Context, req *contractv1.RollbackRequest) (*contractv1.RollbackResponse, error) {
	session, err := h.openEdgeSession(ctx, edge.ClassProduction, req.GetSlug(), req.GetEdge())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	history, err := session.stack.Ledger().History(ctx, "")
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	target, err := rollbackTarget(history, req.GetTo(), req.GetTag())
	if err != nil {
		return nil, provider.RefusalError(err)
	}

	flip := session.front.Facts().FlipBound
	promoted := edge.Promotion{
		PromotionID: target.PromotionID,
		Ts:          time.Now().Unix(),
		Builds:      target.Builds,
		Tag:         target.Tag,
		Flip:        &flip,
	}
	if err := session.stack.Promote(ctx, promoted, "", edge.DiscardProgress()); err != nil {
		return nil, provider.RefusalError(err)
	}
	if err := session.checkpoint(ctx); err != nil {
		return nil, provider.RefusalError(err)
	}
	return &contractv1.RollbackResponse{Promoted: promotionProto(promoted)}, nil
}

func rollbackTarget(history []edge.HistoryEntry, to, tag string) (edge.Promotion, error) {
	if tag != "" {
		for _, entry := range history {
			if entry.Tag == tag {
				return entry.Promotion, nil
			}
		}
		return edge.Promotion{}, refusal.Refuse(refusal.CodeInvalid, "no promotion tagged %q in this project's history", tag)
	}
	if to != "" {
		for _, entry := range history {
			if entry.PromotionID == to {
				return entry.Promotion, nil
			}
		}
		return edge.Promotion{}, refusal.Refuse(refusal.CodeInvalid, "no promotion %q in this project's history", to)
	}
	for i, entry := range history {
		if !entry.Active {
			continue
		}
		if i+1 >= len(history) {
			return edge.Promotion{}, refusal.Refuse(refusal.CodeNotReady, "this project has no earlier promotion to roll back to")
		}
		return history[i+1].Promotion, nil
	}
	return edge.Promotion{}, refusal.Refuse(refusal.CodeNotReady, "this project has no active promotion to roll back from")
}

func (h *handlers) RemoveStalePromotions(ctx context.Context, req *contractv1.RemoveStalePromotionsRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	return streamed(ctx, stream, naming.UnitPromotion, promotionUnitTitle, progressv1.Phase_PHASE_DELETING, func(_ *eventStream, progress edge.Progress) error {
		class, err := classOf(req.GetEnvironment().GetTier())
		if err != nil {
			return err
		}
		pointer, err := envName(req.GetEnvironment())
		if err != nil {
			return err
		}
		if class == edge.ClassProduction {
			pointer = ""
		}

		session, err := h.openEdgeSession(ctx, class, req.GetSlug(), req.GetEdge())
		if undeployed(err) {
			progress.Say("Nothing to prune.")
			return nil
		}
		if err != nil {
			return err
		}
		progress.Say("Diffing deployments to reclaim")
		pruned, err := session.stack.Ledger().Prune(ctx, int(req.GetKeepN()), pointer)
		if err != nil {
			return err
		}
		if err := session.checkpoint(ctx); err != nil {
			return err
		}
		env := pointer
		if class == edge.ClassProduction {
			env = stackrecords.ProductionEnv
		}
		targets, err := ReclaimTargets(req.GetSlug(), env,
			pruned.RemovedRecordKeys, pruned.SurvivingRecordKeys, pruned.SurvivingPointerRecordKeys)
		if err != nil {
			return err
		}
		if err := reclaim(ctx, session.provider, req.GetSlug(), class, targets, progress); err != nil {
			return err
		}
		for _, line := range pruneLines(pruned) {
			progress.Say(line)
		}
		return nil
	})
}

func undeployed(err error) bool {
	var absent noDeploy
	return errors.As(err, &absent)
}

func pruneLines(result edge.PruneResult) []string {
	if len(result.RemovedPromotionIDs) == 0 {
		return []string{"Nothing to prune."}
	}
	return []string{
		fmt.Sprintf("Reclaimed %d promotion(s): %s", len(result.RemovedPromotionIDs), strings.Join(result.RemovedPromotionIDs, ", ")),
		fmt.Sprintf("Kept %d promotion(s).", len(result.KeptPromotionIDs)),
	}
}

func promotionHistoryProto(history []edge.HistoryEntry) []*contractv1.PromotionHistoryEntry {
	out := make([]*contractv1.PromotionHistoryEntry, 0, len(history))
	for _, entry := range history {
		out = append(out, &contractv1.PromotionHistoryEntry{
			Promotion: promotionProto(entry.Promotion),
			Active:    entry.Active,
		})
	}
	return out
}

func promotionProto(promotion edge.Promotion) *contractv1.Promotion {
	return &contractv1.Promotion{
		PromotionId: promotion.PromotionID,
		Ts:          promotion.Ts,
		Builds:      promotion.Builds,
		Tag:         promotion.Tag,
		FlipBound:   flipBoundProto(promotion.Flip),
	}
}

func flipBoundProto(flip *edge.FlipBound) *progressv1.FlipBound {
	if flip == nil {
		return nil
	}
	return &progressv1.FlipBound{TypicalMs: flip.Typical.Milliseconds(), Published: flip.Published}
}

func newPromotionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("mint a promotion id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
