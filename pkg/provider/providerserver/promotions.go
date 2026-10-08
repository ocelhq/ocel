package providerserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

func (h *handlers) ListPromotions(ctx context.Context, req *contractv1.ListPromotionsRequest) (*contractv1.ListPromotionsResponse, error) {
	session, err := h.openEdgeSession(ctx, environment.TierProduction, req.GetSlug(), req.GetEdge())
	if undeployed(err) {
		return &contractv1.ListPromotionsResponse{}, nil
	}
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	history, err := session.ledger.History(ctx, "")
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	return &contractv1.ListPromotionsResponse{Promotions: promotionHistoryProto(history)}, nil
}

func (h *handlers) Rollback(ctx context.Context, req *contractv1.RollbackRequest) (*contractv1.RollbackResponse, error) {
	session, err := h.openEdgeSession(ctx, environment.TierProduction, req.GetSlug(), req.GetEdge())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	current, err := session.ledger.Read(ctx, "")
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	target, err := rollbackTarget(current.History(), req.GetTo(), req.GetTag())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	promotionID, err := newPromotionID()
	if err != nil {
		return nil, provider.RefusalError(err)
	}

	propagation, err := session.readSlowestPropagation(slices.Collect(maps.Values(session.state.Apps)))
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	promoted := router.Promotion{
		PromotionID: promotionID,
		Ts:          time.Now().Unix(),
		Releases:    target.Releases,
		Propagation: &propagation,
	}
	dropped, err := session.promoteApps(ctx, promoteRequest{replaces: current.Active, rollsBackTo: target.PromotionID, promotion: promoted}, session.readAppRouter, progress.Discard())
	if err != nil {
		return nil, provider.RefusalError(errors.Join(err, session.reclaimDropped(ctx, nil, "", dropped, progress.Discard())))
	}
	if err := session.checkpoint(ctx); err != nil {
		return nil, provider.RefusalError(err)
	}
	rolled := &contractv1.RollbackResponse{Promoted: promotionProto(promoted)}
	if err := session.reclaimDropped(ctx, nil, "", dropped, progress.Discard()); err != nil {
		rolled.Warnings = append(rolled.Warnings, unreclaimedWarning(promoted.PromotionID, err))
	}
	return rolled, nil
}

func rollbackTarget(history []router.HistoryEntry, to, tag string) (router.Promotion, error) {
	if tag != "" {
		for _, entry := range history {
			if entry.Tag == tag {
				return entry.Promotion, nil
			}
		}
		return router.Promotion{}, refusal.Refuse(refusal.CodeInvalid, "no promotion tagged %q in this project's history", tag)
	}
	if to != "" {
		for _, entry := range history {
			if entry.PromotionID == to {
				return entry.Promotion, nil
			}
		}
		return router.Promotion{}, refusal.Refuse(refusal.CodeInvalid, "no promotion %q in this project's history", to)
	}
	for i, entry := range history {
		if !entry.Active {
			continue
		}
		for _, earlier := range history[i+1:] {
			if !earlier.Unpromoted {
				return earlier.Promotion, nil
			}
		}
		return router.Promotion{}, refusal.Refuse(refusal.CodeNotReady, "this project has no earlier promotion that served to roll back to")
	}
	return router.Promotion{}, refusal.Refuse(refusal.CodeNotReady, "this project has no active promotion to roll back from")
}

func (h *handlers) RemoveStalePromotions(ctx context.Context, req *contractv1.RemoveStalePromotionsRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	root := RootSpan(naming.SpanPromotion, req.GetSlug(), pruneTitle(req), progressv1.Phase_PHASE_DESTROY)
	return streamed(ctx, stream, root, func(_ *eventStream, progress progress.Log) error {
		tier, err := decodeTier(req.GetEnvironment().GetTier())
		if err != nil {
			return err
		}
		pointer, err := envName(req.GetEnvironment())
		if err != nil {
			return err
		}
		if tier == environment.TierProduction {
			pointer = ""
		}

		session, err := h.openEdgeSession(ctx, tier, req.GetSlug(), req.GetEdge())
		if undeployed(err) {
			progress.Say(fmt.Sprintf("Nothing to prune: %s has no deploy in %s", req.GetSlug(), environmentPhrase(tier, pointer)))
			return nil
		}
		if err != nil {
			return err
		}
		pruned, err := session.ledger.Prune(ctx, int(req.GetKeepN()), pointer)
		if err != nil {
			return err
		}
		if err := session.removeDeployments(ctx, pruned.DeploymentRemovals, progress); err != nil {
			return err
		}
		if err := session.checkpoint(ctx); err != nil {
			return err
		}
		if err := reclaimUnnamed(ctx, session.provider, nil, session.ledger, pointer, pruned, progress); err != nil {
			return err
		}
		if err := session.forgetRemovedDeployments(ctx, pointer, pruned.DeploymentRemovals); err != nil {
			return err
		}
		for _, line := range pruneLines(pruned) {
			progress.Say(line)
		}
		return nil
	})
}

func pruneTitle(req *contractv1.RemoveStalePromotionsRequest) progress.Title {
	env := req.GetEnvironment()
	where := "production"
	if env.GetTier() == environmentv1.Tier_TIER_PREVIEW {
		where = environmentPhrase(environment.TierPreview, env.GetIdentity())
	}
	return progress.Pruning.Title(fmt.Sprintf("the promotions of %s beyond the newest %d", where, req.GetKeepN()))
}

func undeployed(err error) bool {
	var absent noDeploy
	return errors.As(err, &absent)
}

func pruneLines(result router.PruneResult) []string {
	kept := len(result.KeptPromotionIDs)
	switch {
	case len(result.RemovedPromotionIDs) > 0:
		reclaimed := "Reclaimed " + namedList("promotion", "promotions", result.RemovedPromotionIDs)
		if kept == 0 {
			return []string{reclaimed}
		}
		return []string{fmt.Sprintf("%s, kept %d", reclaimed, kept)}
	case kept == 1:
		return []string{"Nothing to prune: the one promotion is kept"}
	default:
		return []string{fmt.Sprintf("Nothing to prune: all %d promotions are kept", kept)}
	}
}

func promotionHistoryProto(history []router.HistoryEntry) []*contractv1.PromotionHistoryEntry {
	out := make([]*contractv1.PromotionHistoryEntry, 0, len(history))
	for _, entry := range history {
		out = append(out, &contractv1.PromotionHistoryEntry{
			Promotion:  promotionProto(entry.Promotion),
			Active:     entry.Active,
			Unpromoted: entry.Unpromoted,
		})
	}
	return out
}

func promotionProto(promotion router.Promotion) *contractv1.Promotion {
	return &contractv1.Promotion{
		PromotionId: promotion.PromotionID,
		Ts:          promotion.Ts,
		Releases:    promotion.Releases,
		Tag:         promotion.Tag,
		Propagation: propagationProto(promotion.Propagation),
	}
}

func propagationProto(propagation *router.Propagation) *progressv1.Propagation {
	if propagation == nil {
		return nil
	}
	return &progressv1.Propagation{TypicalMs: propagation.Typical.Milliseconds(), Published: propagation.Published}
}

func newPromotionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("mint a promotion id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
