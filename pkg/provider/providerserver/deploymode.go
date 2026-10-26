package providerserver

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type deployMode interface {
	verbs() spanVerbs
	promotionTitle(promotionID, where string) progress.Title
	claim(ctx context.Context, h *handlers, spec provider.DeploySpec, token string) (*leaseHold, error)
	finish(ctx context.Context, r *deployRun, err error) error

	wantedBy(slug string) string
	previewHosts(ctx context.Context, r *deployRun, progress progress.Log) error
	protection(ctx context.Context, r *deployRun, gates gateReading) error
	recordProject(ctx context.Context, r *deployRun) error
	preflight(ctx context.Context, hooks *provider.PreflightHooks, pre provider.DeployPreflight) error
	refuseUnconsented(fresh func() (provider.Plan, error)) error
	recordLifecycle(ctx context.Context, r *deployRun) error

	edge(ctx context.Context, r *deployRun, progress progress.Log) error
	infra(ctx context.Context, r *deployRun, stack provider.StackSpec, held infraHeld, progress progress.Log) error
	currentInfra(ctx context.Context, r *deployRun) error
	app(ctx context.Context, r *deployRun, slot int, release appStack, progress progress.Log) (appProvisioned, error)
	hostnames(ctx context.Context, r *deployRun) error
	promote(ctx context.Context, r *deployRun) (*progressv1.OperationEvent, error)
	reclaim(ctx context.Context, r *deployRun)
}

func deployModeOf(req *contractv1.DeployRequest, apps int) (deployMode, error) {
	if req.GetDry() {
		if req.GetConsented() != nil {
			return nil, refuseConsentedDryDeploy()
		}
		return &planned{drawn: dryRunPlan{apps: make([]provider.Plan, apps)}}, nil
	}
	consented, err := consentedPlanOf(req.GetConsented())
	if err != nil {
		return nil, err
	}
	return applied{consented: consented}, nil
}

type spanVerbs struct {
	routes, infra, app progress.Verb
	onto               string
}

type planned struct {
	drawn dryRunPlan
}

func (*planned) verbs() spanVerbs {
	return spanVerbs{routes: progress.Reading, infra: progress.Planning, app: progress.Planning, onto: "for"}
}

func (*planned) promotionTitle(_, where string) progress.Title {
	return progress.Planning.Title("the promotion to " + where)
}

func (*planned) claim(context.Context, *handlers, provider.DeploySpec, string) (*leaseHold, error) {
	return nil, nil
}

func (*planned) finish(_ context.Context, _ *deployRun, err error) error { return err }

func (*planned) wantedBy(string) string { return "" }

func (*planned) previewHosts(ctx context.Context, r *deployRun, progress progress.Log) error {
	return r.readSignedPreviewHosts(ctx, progress)
}

func (*planned) protection(_ context.Context, r *deployRun, gates gateReading) error {
	enforced, err := gateToEnforce(gates)
	if err != nil {
		return err
	}
	r.protection = &enforced.Gate
	return nil
}

func (*planned) recordProject(context.Context, *deployRun) error { return nil }

func (p *planned) preflight(ctx context.Context, hooks *provider.PreflightHooks, pre provider.DeployPreflight) error {
	drawn, err := hooks.Plan(ctx, pre)
	if err != nil {
		return err
	}
	p.drawn.parts = drawn.Groups
	return nil
}

func (*planned) refuseUnconsented(func() (provider.Plan, error)) error { return nil }

func (*planned) recordLifecycle(context.Context, *deployRun) error { return nil }

func (p *planned) edge(_ context.Context, r *deployRun, _ progress.Log) error {
	p.drawn.edge = r.planEdgeGroup()
	return nil
}

func (p *planned) infra(ctx context.Context, r *deployRun, stack provider.StackSpec, _ infraHeld, progress progress.Log) error {
	drawn, err := r.provider.Stacks().Plan(ctx, stack, progress)
	if err != nil {
		return err
	}
	p.drawn.infra = drawn
	if err := r.planBindings(ctx, stack); err != nil {
		return err
	}
	p.drawn.parameters, err = r.planValuesGroup(ctx)
	return err
}

func (p *planned) currentInfra(ctx context.Context, r *deployRun) error {
	var err error
	p.drawn.parameters, err = r.planValuesGroup(ctx)
	return err
}

func (p *planned) app(ctx context.Context, r *deployRun, slot int, release appStack, progress progress.Log) (appProvisioned, error) {
	drawn, err := r.provider.Stacks().Plan(ctx, release.spec, progress)
	if err != nil {
		return appProvisioned{}, err
	}
	p.drawn.apps[slot] = withReleaseMintedAtDeploy(drawn, release.entry.Release.Token())
	return appProvisioned{}, nil
}

func (*planned) hostnames(context.Context, *deployRun) error { return nil }

func (p *planned) promote(ctx context.Context, r *deployRun) (*progressv1.OperationEvent, error) {
	err := r.spanEvents.run(r.spans.Promotion, func(u *spanRun) error {
		return u.phase(func(progress.Log) error {
			retired, err := planRetirement(ctx, r.provider, r.liveApps(), progress.Discard())
			if err != nil {
				return err
			}
			p.drawn.retired = retired
			p.drawn.promotion = r.planPromotionGroup()
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	r.sender.send(planEvent(ChangePlanProto(p.drawn.plan(), r.spec.Slug, string(r.front.Kind()))))
	return okResult(), nil
}

func (*planned) reclaim(context.Context, *deployRun) {}

type applied struct {
	consented *provider.Plan
}

func (applied) verbs() spanVerbs {
	return spanVerbs{routes: progress.Reconciling, infra: progress.Provisioning, app: progress.Deploying, onto: "to"}
}

func (applied) promotionTitle(promotionID, _ string) progress.Title {
	return progress.Switching.Title("traffic to promotion " + promotionID)
}

func (applied) claim(ctx context.Context, h *handlers, spec provider.DeploySpec, token string) (*leaseHold, error) {
	return h.claimEnvironment(ctx, spec, token)
}

func (applied) finish(ctx context.Context, r *deployRun, err error) error {
	return stackrecords.RecordWantedPart(ctx, r.provider.KeyValues(), r.spec.Tier, r.spec.Slug, err)
}

func (applied) wantedBy(slug string) string { return slug }

func (applied) previewHosts(ctx context.Context, r *deployRun, _ progress.Log) error {
	return r.ensureSignedPreviewHosts(ctx)
}

func (applied) protection(ctx context.Context, r *deployRun, _ gateReading) error {
	return r.enforceGate(ctx)
}

func (applied) recordProject(ctx context.Context, r *deployRun) error {
	if err := r.rememberProject(ctx); err != nil {
		return err
	}
	replaces, err := r.ledger.ActivePromotionID(ctx, r.spec.Pointer)
	if err != nil {
		return err
	}
	r.replaces = replaces
	return nil
}

func (applied) preflight(ctx context.Context, hooks *provider.PreflightHooks, pre provider.DeployPreflight) error {
	return hooks.Apply(ctx, pre)
}

func (applied) recordLifecycle(ctx context.Context, r *deployRun) error {
	return r.ensureLifecycle(ctx)
}

func (applied) edge(ctx context.Context, r *deployRun, progress progress.Log) error {
	return r.reconcileEdge(ctx, progress)
}

func (a applied) infra(ctx context.Context, r *deployRun, stack provider.StackSpec, held infraHeld, progress progress.Log) error {
	if err := a.refuseUnconsentedStack(ctx, r, stack, progress, nil); err != nil {
		return err
	}
	return r.provisionInfraStack(ctx, stack, held, progress)
}

func (applied) currentInfra(context.Context, *deployRun) error { return nil }

func (a applied) app(ctx context.Context, r *deployRun, _ int, release appStack, progress progress.Log) (appProvisioned, error) {
	if err := a.refuseUnconsentedStack(ctx, r, release.spec, progress, &release.entry); err != nil {
		return appProvisioned{}, err
	}
	return r.provisionAppStack(ctx, release, progress)
}

func (applied) hostnames(ctx context.Context, r *deployRun) error {
	return r.attachHostnames(ctx)
}

func (applied) promote(ctx context.Context, r *deployRun) (*progressv1.OperationEvent, error) {
	propagation, err := r.readSlowestPropagation(slices.Collect(maps.Values(r.appRouters)))
	if err != nil {
		return nil, err
	}
	promotion := router.Promotion{
		PromotionID: r.spec.PromotionID,
		Ts:          time.Now().Unix(),
		Releases:    r.spec.Releases,
		Tag:         r.spec.Tag,
		Propagation: &propagation,
		Hosts:       r.listDeploymentHosts(),
	}
	if err := r.promote(ctx, promotion); err != nil {
		return nil, err
	}
	return r.result(promotion, propagation)
}

func (applied) reclaim(ctx context.Context, r *deployRun) {
	r.reclaimOwnRelease(ctx)
}
