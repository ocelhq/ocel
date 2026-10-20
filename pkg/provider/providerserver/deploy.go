package providerserver

import (
	"context"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

const stackVersion = "1"

func (h *handlers) Deploy(ctx context.Context, req *contractv1.DeployRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	if err := h.forwards.awaitClosed(ctx); err != nil {
		return err
	}
	return streamResult(ctx, stream, func(sender *eventStream) (*progressv1.OperationEvent, error) {
		if err := refuseInfraProvisionedDeploy(req); err != nil {
			return nil, err
		}
		spec, err := newDeploySpec(req)
		if err != nil {
			return nil, err
		}
		mode, err := deployModeOf(req, len(spec.Apps))
		if err != nil {
			return nil, err
		}
		hold, err := mode.claim(ctx, h, spec, req.GetLeaseToken())
		if err != nil {
			return nil, err
		}
		if hold != nil {
			defer func() { _ = hold.release(ctx) }()
		}
		run, err := h.openDeploy(ctx, req, spec, mode, sender)
		if err != nil {
			return nil, err
		}
		run.hold = hold
		result, err := run.execute(ctx)
		return result, hold.explain(mode.finish(ctx, run, err))
	})
}

type deployRun struct {
	*edgeSession
	readiness    bootstrapplan.Readiness
	parts        []string
	uses         []buildoutput.Uses
	transforms   []string
	options      provider.Options
	artifactRoot string
	sender       *eventStream
	spanEvents   *spanEvents
	manifest     *contractv1.Manifest
	spec         provider.DeploySpec
	managedBy    string
	lifecycle    stackrecords.Lifecycle
	spans        deploySpans

	wildcard    stackrecords.Wildcard
	previewOn   string
	previewKey  edge.PreviewKey
	previewKeys *openedPreviewKeys
	protection  *gate.Gate
	protect     bool
	builtAlias  string
	aliasToken  string
	aliases     []edge.PreviewHost
	deployment  []edge.PreviewHost
	selection   *contractv1.EdgeSelection
	configured  []ConfiguredHost
	pending     []string

	failureNotes []string

	values    variablestore.Store
	scope     variablestore.Scope
	published *publishedBindings

	inline        []inlineBinding
	inlineRecords inlineRecords

	registry provider.RegistryTarget
	images   provider.ImageStore

	replaces string

	infraProvisioned bool
	infraCurrent     bool

	hold *leaseHold

	mode          deployMode
	allowDegraded []string

	outcomes    []*progressv1.AppResult
	provisioned []appProvisioned

	needs      AppNeedVerdicts
	appRouters map[string]router.Kind
	bindings   []provider.Binding
}

func newDeploySpec(req *contractv1.DeployRequest) (provider.DeploySpec, error) {
	promotionID, err := newPromotionID()
	if err != nil {
		return provider.DeploySpec{}, err
	}
	return buildDeploySpec(req, promotionID)
}

func (h *handlers) openDeploy(ctx context.Context, req *contractv1.DeployRequest, spec provider.DeploySpec, mode deployMode, sender *eventStream) (*deployRun, error) {
	p, readiness, err := h.readiness(req.GetEdge().GetKind())
	if err != nil {
		return nil, err
	}
	inline, err := readInlineBindings(req)
	if err != nil {
		return nil, err
	}
	front, err := h.edgeFor(p, req.GetEdge())
	if err != nil {
		return nil, err
	}
	shared, err := openSharedStack(p, front, spec.Tier, spec.Slug)
	if err != nil {
		return nil, err
	}
	outputRoot, err := readOutputRoot(req)
	if err != nil {
		return nil, err
	}
	uses, err := usesOf(outputRoot, req.GetManifest())
	if err != nil {
		return nil, err
	}
	parts := bootstrapplan.RequiredParts(readiness.Bootstrap.Parts(), readiness.Edge)
	run := &deployRun{
		edgeSession: &edgeSession{
			sharedStack: shared,
			tier:        spec.Tier,
			provider:    p,
			store:       edgeStateStore{keyValues: p.KeyValues(), name: stackrecords.EdgeStackKey(spec.Tier, spec.Slug)},
			tunnel:      readTunnelToOrigin(front),
		},
		readiness:    readiness,
		previewKeys:  &h.session.previewKeys,
		parts:        parts,
		uses:         uses,
		transforms:   h.session.transforms(),
		options:      h.session.options(),
		artifactRoot: outputRoot,
		sender:       sender,
		spanEvents:   newSpanEvents(sender),
		manifest:     req.GetManifest(),
		spec:         spec,
		managedBy:    h.session.managedBy(),
		lifecycle:    readPreviewLifecycle(req.GetEnvironment()),
		selection:    req.GetEdge(),
		builtAlias:   req.GetAliasToken(),
		values:       variablestore.Store{KeyValues: p.KeyValues(), Cipher: p.Cipher()},
		scope:        variablestore.Scope{Project: spec.Slug, Tier: spec.Tier},
		inline:       inline,

		infraProvisioned: req.GetInfraProvisioned(),

		mode:          mode,
		allowDegraded: req.GetEdge().GetAllowDegraded(),
		protect:       req.GetProtect(),
	}
	if err := run.openImages(ctx, req.GetProjectRegistry()); err != nil {
		return nil, err
	}
	run.published = &publishedBindings{store: run.values, scope: run.scope, environment: bindingEnvironment(spec)}
	if run.state, err = run.store.read(ctx); err != nil {
		return nil, err
	}
	if err := refuseRoutersRecordedBeforeTheyWereForwarded(p, front.Kind(), run.state); err != nil {
		return nil, err
	}
	shared.restoreRouterStates(run.state)
	run.spans = run.newSpans()
	run.outcomes = pendingOutcomes(spec.Apps)
	run.provisioned = make([]appProvisioned, len(spec.Apps))
	sender.detailing(run.reportFailure)
	return run, nil
}

func pendingOutcomes(apps []provider.AppEntry) []*progressv1.AppResult {
	outcomes := make([]*progressv1.AppResult, len(apps))
	for slot, entry := range apps {
		outcomes[slot] = &progressv1.AppResult{App: entry.App, Outcome: progressv1.AppOutcome_APP_OUTCOME_NOT_RUN}
	}
	return outcomes
}

func (r *deployRun) reportApps(result *progressv1.OperationResult) {
	result.Apps = r.outcomes
}

func (r *deployRun) reportFailure(result *progressv1.OperationResult) {
	r.reportApps(result)
	result.UrlNotes = r.failureNotes
}

func (r *deployRun) execute(ctx context.Context) (*progressv1.OperationEvent, error) {
	leased := r.hold.context(ctx)
	if err := r.spanEvents.run(r.spans.Environment, func(env *spanRun) error {
		return env.phase(func(progress progress.Log) error {
			return r.prepare(leased, progress)
		})
	}); err != nil {
		return nil, err
	}
	if err := r.spanEvents.run(r.spans.Edge, func(u *spanRun) error {
		return u.phase(func(progress progress.Log) error {
			return r.mode.edge(leased, r, progress)
		})
	}); err != nil {
		return nil, err
	}
	if err := r.provision(leased); err != nil {
		r.mode.reclaim(ctx, r)
		return nil, err
	}
	if err := r.mode.hostnames(leased, r); err != nil {
		r.mode.reclaim(ctx, r)
		return nil, err
	}
	if err := r.hold.confirm(ctx); err != nil {
		r.mode.reclaim(ctx, r)
		return nil, err
	}
	result, err := r.mode.promote(leased, r)
	if err != nil {
		r.mode.reclaim(ctx, r)
	}
	return result, err
}

func (r *deployRun) ref(stack naming.StackName) provider.StackRef {
	return provider.StackRef{Project: r.spec.Slug, Tier: r.spec.Tier, Name: stack}
}

func (r *deployRun) teardown() provider.StackDestroy {
	return provider.StackDestroy{Images: r.images, Lease: r.hold.lease()}
}

func (r *deployRun) publishedBindings() *publishedBindings { return r.published }

func (r *deployRun) readPairedRouter(app string) router.Router {
	return r.routers[r.appRouters[app]]
}

func (r *deployRun) addressesItself(app string) bool {
	paired := r.readPairedRouter(app)
	return paired != nil && paired.Facts().AddressesItself
}

func (r *deployRun) readConfiguredRouter(app string) router.Kind {
	if app == "" {
		return r.edgeKind
	}
	return r.appRouters[app]
}
