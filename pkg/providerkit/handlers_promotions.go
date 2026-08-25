package providerkit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (h *handlers) ListEnvironments(ctx context.Context, req *contractv1.ListEnvironmentsRequest) (*contractv1.ListEnvironmentsResponse, error) {
	provider, err := h.session.use()
	if err != nil {
		return nil, err
	}
	environments, err := previewEnvironments(ctx, provider.Records(), req.GetSlug())
	if err != nil {
		return nil, RefusalError(err)
	}
	resp := &contractv1.ListEnvironmentsResponse{Environments: make([]*contractv1.PreviewEnvironment, 0, len(environments))}
	for _, environment := range environments {
		resp.Environments = append(resp.Environments, &contractv1.PreviewEnvironment{
			Identity:  environment.Identity,
			Lifecycle: lifecycleOf(environment.Persisted),
		})
	}
	return resp, nil
}

func (h *handlers) RemoveEnvironment(ctx context.Context, req *contractv1.RemoveEnvironmentRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	return streamed(ctx, stream, progressv1.Phase_PHASE_UNSPECIFIED, func(_ *eventSender, report Reporter) error {
		pointer, err := envName(req.GetEnvironment())
		if err != nil {
			return err
		}
		if pointer == ProductionEnv {
			return Refuse(CodeInvalid,
				"production is not an environment to remove; `ocel destroy production` removes the project's production footprint")
		}
		session, err := h.openStack(ctx, ClassPreview, req.GetSlug(), req.GetEdge())
		if err != nil {
			return err
		}
		report.Say(fmt.Sprintf("Removing preview pointer %q from the store", pointer))
		removed, err := session.stack.RemovePointer(ctx, pointer)
		if err != nil {
			return err
		}
		if err := session.checkpoint(ctx); err != nil {
			return err
		}
		targets, err := ReclaimTargets(req.GetSlug(), pointer,
			removed.RemovedRecordKeys, removed.SurvivingRecordKeys, removed.SurvivingPointerRecordKeys)
		if err != nil {
			return err
		}
		if err := reclaim(ctx, session.provider, req.GetSlug(), ClassPreview, targets, report); err != nil {
			return err
		}
		if err := removePreviewInfra(ctx, session.provider, req.GetSlug(), pointer, report); err != nil {
			return err
		}
		for _, line := range pruneLines(removed) {
			report.Say(line)
		}
		return nil
	})
}

func (h *handlers) ListPromotions(ctx context.Context, req *contractv1.ListPromotionsRequest) (*contractv1.ListPromotionsResponse, error) {
	session, err := h.openStack(ctx, ClassProduction, req.GetSlug(), req.GetEdge())
	if undeployed(err) {
		return &contractv1.ListPromotionsResponse{}, nil
	}
	if err != nil {
		return nil, RefusalError(err)
	}
	history, err := session.stack.Ledger().History(ctx, "")
	if err != nil {
		return nil, RefusalError(err)
	}
	return &contractv1.ListPromotionsResponse{Promotions: promotionHistoryProto(history)}, nil
}

func (h *handlers) Rollback(ctx context.Context, req *contractv1.RollbackRequest) (*contractv1.RollbackResponse, error) {
	session, err := h.openStack(ctx, ClassProduction, req.GetSlug(), req.GetEdge())
	if err != nil {
		return nil, RefusalError(err)
	}
	history, err := session.stack.Ledger().History(ctx, "")
	if err != nil {
		return nil, RefusalError(err)
	}
	target, err := rollbackTarget(history, req.GetTo(), req.GetTag())
	if err != nil {
		return nil, RefusalError(err)
	}

	flip := session.front.FlipBound()
	promoted := edge.Promotion{
		PromotionID: target.PromotionID,
		Ts:          time.Now().Unix(),
		Builds:      target.Builds,
		Tag:         target.Tag,
		Flip:        &flip,
	}
	if err := session.stack.Promote(ctx, promoted, ""); err != nil {
		return nil, RefusalError(err)
	}
	if err := session.checkpoint(ctx); err != nil {
		return nil, RefusalError(err)
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
		return edge.Promotion{}, Refuse(CodeInvalid, "no promotion tagged %q in this project's history", tag)
	}
	if to != "" {
		for _, entry := range history {
			if entry.PromotionID == to {
				return entry.Promotion, nil
			}
		}
		return edge.Promotion{}, Refuse(CodeInvalid, "no promotion %q in this project's history", to)
	}
	for i, entry := range history {
		if !entry.Active {
			continue
		}
		if i+1 >= len(history) {
			return edge.Promotion{}, Refuse(CodeNotReady, "this project has no earlier promotion to roll back to")
		}
		return history[i+1].Promotion, nil
	}
	return edge.Promotion{}, Refuse(CodeNotReady, "this project has no active promotion to roll back from")
}

func (h *handlers) RemoveStalePromotions(ctx context.Context, req *contractv1.RemoveStalePromotionsRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	return streamed(ctx, stream, progressv1.Phase_PHASE_UNSPECIFIED, func(_ *eventSender, report Reporter) error {
		class, err := classOf(req.GetEnvironment().GetTier())
		if err != nil {
			return err
		}
		pointer, err := envName(req.GetEnvironment())
		if err != nil {
			return err
		}
		if class == ClassProduction {
			pointer = ""
		}

		session, err := h.openStack(ctx, class, req.GetSlug(), req.GetEdge())
		if undeployed(err) {
			report.Say("Nothing to prune.")
			return nil
		}
		if err != nil {
			return err
		}
		report.Say("Diffing deployments to reclaim")
		pruned, err := session.stack.Ledger().Prune(ctx, int(req.GetKeepN()), pointer)
		if err != nil {
			return err
		}
		if err := session.checkpoint(ctx); err != nil {
			return err
		}
		env := pointer
		if class == ClassProduction {
			env = ProductionEnv
		}
		targets, err := ReclaimTargets(req.GetSlug(), env,
			pruned.RemovedRecordKeys, pruned.SurvivingRecordKeys, pruned.SurvivingPointerRecordKeys)
		if err != nil {
			return err
		}
		if err := reclaim(ctx, session.provider, req.GetSlug(), class, targets, report); err != nil {
			return err
		}
		for _, line := range pruneLines(pruned) {
			report.Say(line)
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

func removePreviewInfra(ctx context.Context, provider Provider, slug, pointer string, report Reporter) error {
	stack := naming.InfraStack(pointer)
	_, standing, err := ReadStack(ctx, provider.Records(), ClassPreview, slug, stack)
	if err != nil || !standing {
		return err
	}
	report.Say("Destroying " + stack.String())
	if err := provider.Releases().Destroy(ctx, StackRef{Project: slug, Class: ClassPreview, Name: stack}, report); err != nil {
		return fmt.Errorf("destroy %s: %w", stack, err)
	}
	return ForgetStack(ctx, provider.Records(), ClassPreview, slug, stack)
}
