package providerserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func (r *deployRun) promote(ctx context.Context, promotion router.Promotion) error {
	return r.spanEvents.run(r.spans.Promotion, func(u *spanRun) error {
		return u.phase(func(progress progress.Log) error {
			if err := r.forwardAppPreviews(ctx, progress); err != nil {
				return err
			}
			if err := r.publishInlineBindings(ctx); err != nil {
				r.restoreInlineBindings(ctx, progress)
				return err
			}
			previous, superseded, err := r.recordAliases(ctx)
			if err != nil {
				r.restoreInlineBindings(ctx, progress)
				return err
			}
			dropped, err := r.promoteApps(ctx, promoteRequest{pointer: r.spec.Pointer, hosts: r.aliases, superseded: superseded, previous: previous, replaces: r.replaces, promotion: promotion, confirm: r.hold.confirm}, r.readAppRouter, progress)
			if err != nil {
				r.restoreInlineBindings(ctx, progress)
				return errors.Join(err, r.restoreAliasesUnlessPromoted(ctx, promotion.PromotionID, previous), r.reclaimDropped(ctx, r.teardown(), r.spec.Pointer, dropped, progress))
			}
			if err := r.serveDeployment(ctx, r.spec.Pointer, promotion, r.readAppRouter, progress); err != nil {
				progress.Warn(fmt.Sprintf("Promotion %s serves on %s, but not on its own deployment hostname: %v",
					promotion.PromotionID, joinNames(edge.ListPreviewHostnames(r.aliases)), err))
				r.deployment = nil
			}
			if err := r.checkpointPromoted(ctx); err != nil {
				return err
			}
			if err := r.reclaimDropped(ctx, r.teardown(), r.spec.Pointer, dropped, progress); err != nil {
				progress.Warn(unreclaimedWarning(promotion.PromotionID, err))
			}
			if err := retire(ctx, r.provider, r.liveApps(), progress); err != nil {
				progress.Warn(unretiredWarning(promotion.PromotionID, err))
			}
			r.pruneInlineBindings(ctx, progress)
			return r.recordPromotedEnvironment(ctx, superseded)
		})
	})
}

func (r *deployRun) checkpointPromoted(ctx context.Context) error {
	ctx, stop := newRecordContext(ctx)
	defer stop()
	return r.checkpoint(ctx)
}

func (r *deployRun) recordPromotedEnvironment(ctx context.Context, superseded []edge.PreviewHost) error {
	if r.spec.Tier != environment.TierPreview {
		return nil
	}
	ctx, stop := newRecordContext(ctx)
	defer stop()
	if err := stackrecords.ForgetSuperseded(ctx, r.provider.KeyValues(), r.spec.Tier, r.spec.Slug, r.spec.Env, superseded); err != nil {
		return err
	}
	return stackrecords.RecordEnvironmentMeta(ctx, r.provider.KeyValues(),
		r.spec.Tier, r.spec.Slug, r.spec.Env, r.aliasToken, r.spec.Label, r.lifecycle)
}

func (r *deployRun) recordAliases(ctx context.Context) (previous, superseded []edge.PreviewHost, err error) {
	if r.spec.Tier != environment.TierPreview {
		return nil, nil, nil
	}
	return stackrecords.RecordAliases(ctx, r.provider.KeyValues(), r.spec.Tier, r.spec.Slug, r.spec.Env, r.aliasToken, r.aliases)
}

func (r *deployRun) restoreAliasesUnlessPromoted(ctx context.Context, promotionID string, previous []edge.PreviewHost) error {
	if r.spec.Tier != environment.TierPreview {
		return nil
	}
	ctx, stop := newRecordContext(ctx)
	defer stop()
	active, err := r.ledger.ActivePromotionID(ctx, r.spec.Pointer)
	if err != nil {
		return err
	}
	if active == promotionID {
		return nil
	}
	return stackrecords.RestoreAliases(ctx, r.provider.KeyValues(), r.spec.Tier, r.spec.Slug, r.spec.Env, r.aliasToken, r.aliases, previous)
}

func (r *deployRun) result(promotion router.Promotion, propagation router.Propagation) (*progressv1.OperationEvent, error) {
	result := &progressv1.OperationResult{
		Success:           true,
		PromotionId:       promotion.PromotionID,
		PromotedAtSeconds: promotion.Ts,
		Propagation:       propagationProto(&propagation),
	}
	r.reportApps(result)
	for slot, hosts := range r.listServedHostnames() {
		for _, host := range hosts {
			if r.hostingMode() == hostingProduction && !r.state.Ready(host, r.front.Kind(), r.readConfiguredRouter(r.spec.Apps[slot].App)) {
				continue
			}
			r.outcomes[slot].Urls = append(r.outcomes[slot].Urls, "https://"+host)
		}
	}
	for slot, entry := range r.spec.Apps {
		r.outcomes[slot].Release = entry.Release.String()
		r.outcomes[slot].StorageCoordinate = appCoordinate(r.spec, entry.App, entry.Release.Token()).StorageCoordinate()
		if len(r.outcomes[slot].Urls) == 0 && r.addressesItself(entry.App) {
			if address := r.provisioned[slot].address; address != "" {
				r.outcomes[slot].Urls = append(r.outcomes[slot].Urls, address)
			}
		}
		if host := findAppHost(r.listDeploymentHosts(), entry.App); host.Hostname != "" {
			r.outcomes[slot].DeploymentUrl = "https://" + host.Hostname
		}
	}
	result.UrlNotes = r.pending
	return &progressv1.OperationEvent{Body: &progressv1.OperationEvent_Result{Result: result}}, nil
}

func (r *deployRun) listDeploymentHosts() []edge.PreviewHost {
	var hosts []edge.PreviewHost
	for _, entry := range r.spec.Apps {
		paired := r.readPairedRouter(entry.App)
		if !paired.Facts().ServesPreviewDeployments {
			continue
		}
		if host := findAppHost(r.deployment, entry.App); host.Hostname != "" {
			hosts = append(hosts, host)
		}
	}
	return hosts
}
