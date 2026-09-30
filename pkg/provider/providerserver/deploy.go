package providerserver

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	connect "connectrpc.com/connect"
	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/progress"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

const stackVersion = "1"

func (h *handlers) Deploy(ctx context.Context, req *contractv1.DeployRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	return streamResult(ctx, stream, func(sender *eventStream) (*progressv1.OperationEvent, error) {
		run, err := h.openDeploy(ctx, req, sender)
		if err != nil {
			return nil, err
		}
		return run.execute(ctx)
	})
}

type deploySpans struct {
	Environment Span
	Infra       Span
	Apps        map[string]Span
	Edge        Span
	Hostnames   Span
	Promotion   Span
}

func (r *deployRun) newSpans() deploySpans {
	env, kind := environmentSubject(r.spec.Tier, r.spec.Env), string(r.front.Kind())
	where := environmentPhrase(r.spec.Tier, r.spec.Env)
	routes, infra, app, onto := progress.Reconciling, progress.Provisioning, progress.Deploying, "to"
	if r.dry {
		routes, infra, app, onto = progress.Reading, progress.Planning, progress.Planning, "for"
	}
	s := deploySpans{
		Environment: RootSpan(naming.SpanEnvironment, env,
			progress.Checking.Title("the bootstrap, domains and bindings for "+r.spec.Slug), progressv1.Phase_PHASE_PROVISION),
		Infra: RootSpan(r.spec.Infra.String(), env, infra.Title(r.describeInfra()), progressv1.Phase_PHASE_PROVISION),
		Edge:  RootSpan(naming.SpanEdge, kind, routes.Title(fmt.Sprintf("the routes for %s in %s", r.spec.Slug, where)), progressv1.Phase_PHASE_PROVISION),
		Hostnames: RootSpan(naming.SpanHostnames, kind,
			progress.Attaching.Title(namedList("production hostname", "production hostnames", r.hostnames())), progressv1.Phase_PHASE_PROVISION),
		Promotion: RootSpan(naming.SpanPromotion, env,
			progress.Switching.Title("traffic to promotion "+r.spec.PromotionID), progressv1.Phase_PHASE_PROMOTE),
		Apps: make(map[string]Span, len(r.spec.Apps)),
	}
	for _, entry := range r.spec.Apps {
		s.Apps[entry.App] = RootSpan(entry.Stack.String(), entry.App,
			app.Title(fmt.Sprintf("the %s %s %s", appNoun(entry), onto, where)), progressv1.Phase_PHASE_DEPLOY)
	}
	return s
}

func (r *deployRun) describeInfra() string {
	names := r.manifestResourceNames()
	if len(names) == 0 {
		return "stack " + r.spec.Infra.String()
	}
	return namedList("shared resource", "shared resources", names)
}

func (r *deployRun) manifestResourceNames() []string {
	names := make([]string, 0, len(r.manifest.GetResources()))
	for _, resource := range r.manifest.GetResources() {
		names = append(names, cmp.Or(resource.GetLogicalName(), resource.GetResource().GetName()))
	}
	return names
}

func appNoun(entry provider.AppEntry) string {
	if entry.Compute() == "" {
		return "app"
	}
	return string(entry.Compute()) + " app"
}

func environmentSubject(tier environment.Tier, env string) string {
	if tier == environment.TierPreview {
		return env
	}
	return string(environment.TierProduction)
}

func environmentPhrase(tier environment.Tier, env string) string {
	if tier == environment.TierPreview {
		return "preview " + env
	}
	return string(environment.TierProduction)
}

func namedList(one, many string, names []string) string {
	switch len(names) {
	case 0:
		return "no " + many
	case 1:
		return one + " " + names[0]
	default:
		return many + " " + joinNames(names)
	}
}

func joinNames(names []string) string {
	const shown = 3
	switch n := len(names); {
	case n <= 1:
		return strings.Join(names, "")
	case n <= shown+1:
		return strings.Join(names[:n-1], ", ") + " and " + names[n-1]
	default:
		return fmt.Sprintf("%s and %d more", strings.Join(names[:shown], ", "), n-shown)
	}
}

type deployRun struct {
	*edgeSession
	gate         Gate
	features     []string
	transforms   []string
	artifactRoot string
	sender       *eventStream
	spanEvents   *spanEvents
	manifest     *contractv1.Manifest
	spec         provider.DeploySpec
	spans        deploySpans

	wildcard   stackrecords.Wildcard
	previewOn  string
	selection  *contractv1.EdgeSelection
	configured []ConfiguredHost
	pending    []string

	values    variablestore.Store
	scope     variablestore.Scope
	published *publishedBindings

	inline        []inlineBinding
	inlineRecords inlineRecords

	registry provider.RegistryTarget
	images   provider.ImageStore

	replaces string

	dry           bool
	dryRunPlan    dryRunPlan
	allowDegraded []string

	outcomes []*progressv1.AppResult

	mu             sync.Mutex
	artifacts      map[string]provider.ArtifactRef
	functionImages map[string]string
	needs          AppNeedVerdicts
	appRouters     map[string]router.Kind
	bindings       []provider.Binding
	provisioning   map[string]bool
}

func (r *deployRun) recordArtifact(logical string, ref provider.ArtifactRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.artifacts[logical] = ref
}

func (r *deployRun) artifact(logical string) (provider.ArtifactRef, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, ok := r.artifacts[logical]
	return ref, ok
}

func (r *deployRun) recordProvisioning(app string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.provisioning[app] = true
}

func (r *deployRun) isProvisioning(app string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.provisioning[app]
}

func (h *handlers) openDeploy(ctx context.Context, req *contractv1.DeployRequest, sender *eventStream) (*deployRun, error) {
	p, gate, err := h.gate(req.GetEdge().GetKind())
	if err != nil {
		return nil, err
	}
	promotionID, err := newPromotionID()
	if err != nil {
		return nil, err
	}
	spec, err := buildDeploySpec(req, promotionID)
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
	features, err := bootstrapplan.RequiredFeatures(gate.Bootstrap.Catalogue(), frameworksOf(req.GetManifest()), gate.Edge)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	run := &deployRun{
		edgeSession: &edgeSession{
			sharedStack: shared,
			provider:    p,
			store:       edgeStateStore{keyValues: p.KeyValues(), name: stackrecords.EdgeStackKey(spec.Tier, spec.Slug)},
			tunnel:      tunnelThrough(front, req.GetEdge()),
		},
		gate:           gate,
		features:       features,
		transforms:     h.session.transforms(),
		artifactRoot:   h.session.artifactRoot(),
		sender:         sender,
		spanEvents:     newSpanEvents(sender),
		manifest:       req.GetManifest(),
		spec:           spec,
		selection:      req.GetEdge(),
		values:         variablestore.Store{KeyValues: p.KeyValues(), Cipher: p.Cipher()},
		scope:          variablestore.Scope{Project: spec.Slug, Tier: spec.Tier},
		artifacts:      map[string]provider.ArtifactRef{},
		functionImages: map[string]string{},
		provisioning:   map[string]bool{},
		inline:         inline,

		dry:           req.GetDry(),
		allowDegraded: req.GetEdge().GetAllowDegraded(),
	}
	if err := run.openImages(ctx, req.GetProjectRegistry()); err != nil {
		return nil, err
	}
	run.published = &publishedBindings{store: run.values, scope: run.scope, environment: bindingEnvironment(spec)}
	if run.state, err = run.store.read(ctx); err != nil {
		return nil, err
	}
	run.spans = run.newSpans()
	run.outcomes = pendingOutcomes(spec.Apps)
	run.dryRunPlan.apps = make([]provider.Plan, len(spec.Apps))
	sender.detailing(run.reportApps)
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

func (r *deployRun) execute(ctx context.Context) (*progressv1.OperationEvent, error) {
	if err := r.spanEvents.run(r.spans.Environment, func(env *spanRun) error {
		return env.phase(func(progress progress.Log) error {
			return r.prepare(ctx, progress)
		})
	}); err != nil {
		return nil, err
	}
	if err := r.reconcileEdgeSpan(ctx); err != nil {
		return nil, err
	}
	if err := r.provision(ctx); err != nil {
		r.reclaimOwnRelease(ctx)
		return nil, err
	}
	if err := r.attachHostnames(ctx); err != nil {
		r.reclaimOwnRelease(ctx)
		return nil, err
	}
	result, err := r.promote(ctx)
	if err != nil {
		r.reclaimOwnRelease(ctx)
	}
	return result, err
}

func (r *deployRun) prepare(ctx context.Context, progress progress.Log) error {
	if err := r.checkInlineBindings(ctx, progress); err != nil {
		return err
	}
	if err := r.ensureBootstrap(ctx, progress); err != nil {
		return err
	}
	if err := r.resolveServingDomains(ctx); err != nil {
		return err
	}
	if err := r.readInlineRecords(ctx); err != nil {
		return err
	}
	if err := r.admitBindings(ctx, progress); err != nil {
		return err
	}
	if !r.dry {
		if err := r.rememberProject(ctx); err != nil {
			return err
		}
		replaces, err := r.ledger.ActivePromotionID(ctx, r.spec.Pointer)
		if err != nil {
			return err
		}
		r.replaces = replaces
	}
	if err := r.checkNeeds(ctx); err != nil {
		return err
	}
	if err := r.preflight(ctx, progress); err != nil {
		return err
	}
	r.appRouters = pairApps(r.provider.Facts(), r.front.Kind(), r.spec.Apps)
	return nil
}

func (r *deployRun) ensureBootstrap(ctx context.Context, progress progress.Log) error {
	_, err := r.gate.EnsureReady(ctx, r.spec.Tier, r.features, !r.dry, progress)
	return err
}

func (r *deployRun) resolveServingDomains(ctx context.Context) error {
	hosts := r.hostnames()
	if r.spec.Tier == environment.TierPreview {
		wildcard, err := stackrecords.ReadWildcard(ctx, r.provider.KeyValues())
		if err != nil {
			return err
		}
		r.wildcard = wildcard
		if len(hosts) > 0 {
			base, err := r.previewBase()
			if err != nil {
				return err
			}
			r.previewOn = base
			return nil
		}
		if wildcard.BaseDomain != "" {
			r.previewOn = wildcard.BaseDomain
			return nil
		}
		return refusal.Refuse(refusal.CodeNotReady,
			"this project declares no domains.preview wildcard and no global preview domain is in use, so a preview deploy has nowhere to serve: "+
				"declare a project-level domains.preview wildcard, or run `ocel domain use '*.preview.example.com' --preview` to serve every project's previews on one wildcard")
	}
	if len(hosts) == 0 {
		if r.router.Facts().AddressesItself {
			return nil
		}
		return refusal.Refuse(refusal.CodeNotReady,
			"no domains.production declared on the project or any app, so this deploy has nowhere to serve: declare one, and the deploy that reads it attaches it")
	}
	configured, err := r.configuredHosts()
	if err != nil {
		return err
	}
	r.configured = configured
	writer, err := dnsFor(r.provider, r.front, r.selection)
	if err != nil {
		return err
	}
	r.installDNSCutover(writer, r.selection.GetDns().GetZone())
	r.cutover.manual = failOnManualRecords(r.sender, r.spans.Hostnames)
	return nil
}

func (r *deployRun) rememberProject(ctx context.Context) error {
	name := stackrecords.ProjectKey(r.spec.Tier, r.spec.Slug)
	recorded, err := keyvalue.ReadOrEmpty(ctx, r.provider.KeyValues(), name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	if recorded.Value, err = json.Marshal(stackrecords.Project{Features: r.features}); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	if _, err := r.provider.KeyValues().Write(ctx, recorded); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return nil
}

type hostingMode int

const (
	hostingProduction hostingMode = iota
	hostingGlobalPreview
	hostingProjectPreview
)

func (r *deployRun) hostingMode() hostingMode {
	if r.spec.Tier != environment.TierPreview {
		return hostingProduction
	}
	if r.wildcard.BaseDomain != "" && len(r.hostnames()) == 0 {
		return hostingGlobalPreview
	}
	return hostingProjectPreview
}

func (r *deployRun) reconcileEdgeSpan(ctx context.Context) error {
	return r.spanEvents.run(r.spans.Edge, func(u *spanRun) error {
		return u.phase(func(progress progress.Log) error {
			if r.dry {
				r.dryRunPlan.edge = r.planEdgeGroup()
				return nil
			}
			return r.reconcileEdge(ctx, progress)
		})
	})
}

func (r *deployRun) reconcileEdge(ctx context.Context, progress progress.Log) error {
	spec := edge.StackSpec{
		Version:     stackVersion,
		Tier:        r.spec.Tier,
		Slug:        r.spec.Slug,
		PruneRoutes: true,
		Warn:        progress.Warn,
	}
	var base string
	switch r.hostingMode() {
	case hostingProduction:
		spec.Domains = r.hostnames()
		spec.DomainApps = r.domainApps()
	case hostingGlobalPreview:
		spec.PruneOnly = true
	default:
		if len(r.spec.Apps) == 0 {
			spec.PruneOnly = true
			break
		}
		base = r.previewOn
		spec.Domains = []string{edge.PreviewWildcard(base)}
	}
	program, err := edgeProgramFor(ctx, r.provider, r.front, provider.EdgeProgramRequest{
		Tier:              r.spec.Tier,
		Slug:              r.spec.Slug,
		Env:               r.spec.Env,
		PreviewBaseDomain: base,
		Apps:              r.appNames(),
	})
	if err != nil {
		return err
	}
	spec.Program, spec.Values = program.Spec, program.Values
	stack, err := r.front.Reconcile(ctx, spec, r.state.Edge)
	if err != nil {
		return err
	}
	r.setEdgeStack(stack)
	if err := r.checkpoint(ctx); err != nil {
		return err
	}
	return r.forwardPreviews(ctx, progress)
}

func (r *deployRun) forwardPreviews(ctx context.Context, progress progress.Log) error {
	if r.routerOrigin() == nil || r.front.Facts().RunsCode {
		return nil
	}
	switch r.hostingMode() {
	case hostingGlobalPreview:
		shared := &wildcards{provider: r.provider, keyValues: r.provider.KeyValues(), recorded: r.wildcard, sel: r.selection}
		return shared.refreshEntryClaim(ctx, r.front, progress)
	case hostingProjectPreview:
		if r.previewOn == "" || len(r.spec.Apps) == 0 {
			return nil
		}
	default:
		return nil
	}
	target := ConfiguredHost{Hostname: edge.PreviewWildcard(r.previewOn)}
	forwarding := &hostnames{edgeSession: r.edgeSession}
	hostState := r.state.Host(target.Hostname)
	if slices.Contains(r.edgeStack().State().Bound, target.Hostname) {
		_, err := forwarding.refreshOriginClaim(ctx, target, &hostState, progress)
		return err
	}
	return forwarding.bindOrigin(ctx, target, &hostState, progress)
}

func (r *deployRun) attachHostnames(ctx context.Context) error {
	if r.dry || r.hostingMode() != hostingProduction {
		return nil
	}
	return r.spanEvents.run(r.spans.Hostnames, func(u *spanRun) error {
		var attached, missed []string
		err := u.phase(func(progress progress.Log) error {
			attaching := &hostnames{edgeSession: r.edgeSession}
			skip := func(host, note string) {
				progress.Warn(note)
				r.pending = append(r.pending, note)
				missed = append(missed, host)
			}
			for _, host := range r.configured {
				if r.state.Ready(host.Hostname, r.front.Kind(), r.readAppRouter(host.App)) {
					hostState := r.state.Host(host.Hostname)
					if _, err := attaching.refreshOriginClaim(ctx, host, &hostState, progress); err != nil {
						return err
					}
					attached = append(attached, host.Hostname)
					continue
				}
				if bound, served := r.state.Host(host.Hostname).ServedEdge(); served && bound != r.front.Kind() {
					skip(host.Hostname, fmt.Sprintf(
						"%s is still served through %s, not through %s this deploy promoted to: `ocel domain add` moves it, in the order that keeps it answering",
						host.Hostname, describeFront(bound), describeFront(r.front.Kind())))
					continue
				}
				_, err := attaching.attachHostname(ctx, host, progress)
				if waits, ok := provider.ResumableMessage(err); ok {
					skip(host.Hostname, fmt.Sprintf("%s is not served yet: %s", host.Hostname, waits))
					continue
				}
				if err != nil {
					return err
				}
				attached = append(attached, host.Hostname)
			}
			return nil
		})
		if err == nil && len(missed) > 0 {
			u.recordPartial(describeAttached(attached, missed))
		}
		return err
	})
}

func describeAttached(attached, missed []string) string {
	if len(attached) == 0 {
		return "Did not attach " + namedList("production hostname", "production hostnames", missed)
	}
	return "Attached " + namedList("production hostname", "production hostnames", attached) + " but not " + joinNames(missed)
}

func (r *deployRun) configuredHosts() ([]ConfiguredHost, error) {
	owners := r.domainApps()
	hosts := r.hostnames()
	declared := make([]*contractv1.ConfiguredHostname, 0, len(hosts))
	for _, host := range hosts {
		declared = append(declared, &contractv1.ConfiguredHostname{Hostname: host, App: owners[strings.ToLower(host)]})
	}
	return productionHosts(declared)
}

func (r *deployRun) previewBase() (string, error) {
	var base string
	for _, host := range r.hostnames() {
		resolved, wildcard := strings.CutPrefix(host, "*.")
		if !wildcard {
			return "", refusal.Refuse(refusal.CodeInvalid,
				"this project declares the preview domain %q, which is not a `*.` wildcard: every preview is served on its own subdomain of it, so declare %q instead",
				host, edge.PreviewWildcard(host))
		}
		if base != "" && resolved != base {
			return "", refusal.Refuse(refusal.CodeInvalid,
				"this project declares more than one preview domain (%q and %q): a preview domain is claimed by the whole project, "+
					"which serves every app from one wildcard, so declare a single project-level domains.preview",
				edge.PreviewWildcard(base), host)
		}
		base = resolved
	}
	return base, nil
}

func (r *deployRun) globalPreview() string {
	if r.hostingMode() != hostingGlobalPreview {
		return ""
	}
	return r.wildcard.BaseDomain
}

func (r *deployRun) appNames() []string {
	names := make([]string, 0, len(r.spec.Apps))
	for _, entry := range r.spec.Apps {
		if name := strings.ToLower(strings.TrimSpace(entry.App)); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func (r *deployRun) domainApps() map[string]string {
	served := r.servedHostnames()
	owners := make(map[string]string, len(served))
	for slot, hosts := range served {
		name := strings.ToLower(strings.TrimSpace(r.spec.Apps[slot].App))
		if name == "" {
			continue
		}
		for _, host := range hosts {
			owners[strings.ToLower(host)] = name
		}
	}
	if len(owners) == 0 {
		return nil
	}
	return owners
}

func (r *deployRun) hostnames() []string {
	tier := encodeTier(r.spec.Tier)
	seen := map[string]bool{}
	hosts := unseen(tierHostnames(r.manifest.GetDomains(), tier), seen)
	for _, app := range r.manifest.GetApps() {
		hosts = append(hosts, unseen(tierHostnames(app.GetDomains(), tier), seen)...)
	}
	return hosts
}

func tierHostnames(declared []*contractv1.TierDomains, tier environmentv1.Tier) []string {
	var hosts []string
	for _, domains := range declared {
		if domains.GetTier() != tier {
			continue
		}
		hosts = append(hosts, domains.GetHostnames()...)
	}
	return hosts
}

func (r *deployRun) previewSite() edge.PreviewSite {
	if r.hostingMode() == hostingGlobalPreview {
		return edge.SharedPreview(r.spec.Slug, r.previewOn)
	}
	return edge.ProjectPreview(r.previewOn)
}

func (r *deployRun) previewLabel(slot int) string {
	if r.hostingMode() != hostingGlobalPreview || !r.router.Facts().RoutesPreviewsByLabel {
		return ""
	}
	return r.previewSite().Label(r.spec.Pointer, edge.AppAt(r.appNames(), slot))
}

func (r *deployRun) servedHostnames() [][]string {
	if r.spec.Tier == environment.TierPreview {
		served := make([][]string, len(r.spec.Apps))
		site, names := r.previewSite(), r.appNames()
		for slot := range served {
			if host := site.Host(r.spec.Pointer, edge.AppAt(names, slot)); host != "" {
				served[slot] = []string{host}
			}
		}
		return served
	}
	tier := encodeTier(r.spec.Tier)
	own := make([][]string, len(r.spec.Apps))
	for slot, entry := range r.spec.Apps {
		own[slot] = tierHostnames(entry.Manifest.GetDomains(), tier)
	}
	return edge.AttributeHostnames(tierHostnames(r.manifest.GetDomains(), tier), own)
}

func (r *deployRun) checkpoint(ctx context.Context) error {
	r.state.Kind = r.front.Kind()
	r.state.Edge = r.edgeStack().State()
	r.state.Pair(r.router.Kind(), r.routerState(), r.appRouters)
	if r.spec.Tier == environment.TierPreview {
		r.state.Edge.GlobalPreview = r.globalPreview()
	}
	return r.store.write(ctx, r.state)
}

func (r *deployRun) checkNeeds(ctx context.Context) error {
	check := EdgeNeedCheck{
		Edge:          r.front,
		Root:          r.artifactRoot,
		AllowDegraded: r.allowDegraded,
		Degraded: func(app string, need edge.Need, detail string) {
			r.sender.send(degradedEvent(app, need, detail))
		},
		Warn: func(subject, message string) {
			r.sender.send(checkWarning(subject, message))
		},
	}
	verdicts, err := check.Run(ctx, r.manifest)
	if err != nil {
		return err
	}
	r.needs = verdicts
	return nil
}

func (r *deployRun) preflight(ctx context.Context, progress progress.Log) error {
	resources, err := manifestResources(r.manifest)
	if err != nil {
		return err
	}
	grants, err := r.publishedBindings().Published(ctx)
	if err != nil {
		return err
	}
	if err := RefuseUnservedProxiedBindings(r.provider.Facts().Vendor, r.provider.Facts().Bindings, proxied, resources, grants); err != nil {
		return refusal.Refuse(refusal.CodeInvalid, "%s", err)
	}
	if err := r.refuseContainerValues(ctx); err != nil {
		return err
	}
	if facts := r.provider.Facts(); !facts.RendersTransforms {
		if err := provider.RefuseTransforms(facts.Vendor, r.transforms); err != nil {
			return err
		}
	}
	preflightDeploy := r.provider.Hooks().PreflightDeploy
	if preflightDeploy == nil {
		return nil
	}
	apps, err := r.usage(resources, grants)
	if err != nil {
		return err
	}
	return preflightDeploy(ctx, provider.DeployPreflight{
		Deploy:    r.spec,
		Edge:      r.front.Kind(),
		Resources: resources,
		Grants:    grants,
		Apps:      apps,
		Progress:  progress,
		WrittenBy: r.gate.WrittenBy,
		Dry:       r.dry,
	})
}

func (r *deployRun) usage(resources []provider.Resource, published []provider.Binding) ([]provider.AppUsage, error) {
	apps := make([]provider.AppUsage, 0, len(r.spec.Apps))
	for _, entry := range r.spec.Apps {
		used, err := r.boundNamesUsedBy(entry.App)
		if err != nil {
			return nil, err
		}
		usage := provider.AppUsage{App: entry.App}
		for _, resource := range resources {
			if used[boundName(resource)] {
				usage.Resources = append(usage.Resources, resource)
			}
		}
		for _, binding := range published {
			if used[binding.Name] {
				usage.Grants = append(usage.Grants, binding)
			}
		}
		apps = append(apps, usage)
	}
	return apps, nil
}

func boundName(resource provider.Resource) string {
	if resource.Binding != "" {
		return resource.Binding
	}
	return resource.Name
}

func (r *deployRun) provision(ctx context.Context) error {
	if err := r.provisionInfra(ctx); err != nil {
		return err
	}
	return r.provisionApps(ctx)
}

const appConcurrency = 4

func (r *deployRun) provisionApps(ctx context.Context) error {
	failures := make([]error, len(r.spec.Apps))
	var apps errgroup.Group
	apps.SetLimit(appConcurrency)
	for slot, entry := range r.spec.Apps {
		apps.Go(func() error {
			failures[slot] = r.provisionApp(ctx, slot, entry)
			return nil
		})
	}
	_ = apps.Wait()
	var first error
	for slot, err := range failures {
		r.outcomes[slot] = appOutcome(r.spec.Apps[slot].App, err)
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

func appOutcome(app string, err error) *progressv1.AppResult {
	if err != nil {
		return &progressv1.AppResult{
			App:     app,
			Outcome: progressv1.AppOutcome_APP_OUTCOME_FAILED,
			Error:   err.Error(),
		}
	}
	return &progressv1.AppResult{App: app, Outcome: progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED}
}

func (r *deployRun) provisionInfra(ctx context.Context) error {
	if r.spec.Infra.IsZero() {
		return nil
	}
	resources, err := manifestResources(r.manifest)
	if err != nil {
		return err
	}
	return r.spanEvents.run(r.spans.Infra, func(u *spanRun) error {
		return u.phase(func(progress progress.Log) error {
			if err := r.refuseToAdopt(ctx, r.spec.Infra); err != nil {
				return err
			}
			stack := provider.StackSpec{
				Ref:       r.ref(r.spec.Infra),
				Kind:      provider.StackInfra,
				Edge:      r.front,
				Tags:      infraTags(r.spec),
				Resources: resources,
				Bindings:  r.publishedBindings(),
			}
			if r.dry {
				planned, err := r.provider.Stacks().Plan(ctx, stack, progress)
				if err != nil {
					return err
				}
				r.dryRunPlan.infra = planned
				r.dryRunPlan.parameters, err = r.planValuesGroup(ctx)
				return err
			}
			result, err := r.provider.Stacks().Provision(ctx, stack, progress)
			if err != nil {
				return err
			}
			for _, binding := range result.Bindings {
				if err := provider.VerifyProperties(binding); err != nil {
					return err
				}
			}
			if err := r.publish(ctx, result.Bindings); err != nil {
				return err
			}
			r.bindings = result.Bindings
			return stackrecords.Write(ctx, r.provider.KeyValues(), r.spec.Tier, r.spec.Slug, r.spec.Infra, stackrecords.Stack{
				Kind:      provider.StackInfra,
				Bindings:  result.Bindings,
				WrittenBy: provider.WrittenByVersion(""),
			})
		})
	})
}

func (r *deployRun) provisionApp(ctx context.Context, slot int, entry provider.AppEntry) error {
	return r.spanEvents.run(r.spans.Apps[entry.App], func(u *spanRun) error {
		return u.phase(func(progress progress.Log) error {
			if err := r.refuseToAdopt(ctx, entry.Stack); err != nil {
				return err
			}
			grants, err := r.grants(ctx, entry)
			if err != nil {
				return err
			}
			facts, err := r.appServing(entry)
			if err != nil {
				return err
			}
			values, err := r.appValues(ctx, entry, grants)
			if err != nil {
				return err
			}
			pack, err := r.pack(ctx, entry, values, progress)
			if err != nil {
				return err
			}
			staged, functions, err := r.stageFunctions(ctx, entry, pack, facts.OriginDispatch)
			if err != nil {
				return err
			}
			defer discardStaged(staged)
			images, err := r.imagePushes(ctx, entry, functions)
			if err != nil {
				return err
			}
			spec := provider.StackSpec{
				Ref:      r.ref(entry.Stack),
				Kind:     provider.StackApp,
				Edge:     r.front,
				Tags:     appTags(r.spec, entry),
				Uploads:  staged,
				Images:   images,
				Bindings: r.publishedBindings(),
				App: &provider.AppSpec{
					App:             entry.App,
					Framework:       entry.Manifest.GetFramework().GetName(),
					Entry:           entryLogicalName(entry.Manifest, facts.Entry),
					Deployment:      entry.Build.DeploymentID(),
					Compute:         entry.Compute(),
					Router:          r.readAppRouter(entry.App),
					Functions:       r.functionSpecs(entry),
					Image:           imageToRun(images, entry),
					HealthCheckPath: entry.HealthCheckPath,
					Arch:            entry.Arch,
					Values:          values,
					Grants:          grants,
					Routing:         facts.OriginDispatch,
					ISR:             facts.ISR,
					Bytecode:        facts.Bytecode,
					AssetPrefix:     facts.AssetPrefix,
					PreviewLabel:    r.previewLabel(slot),
					Guard:           facts.Guard,
					VendorState:     pack.VendorState,
					Proxied:         anyProxied(proxied, grants),
				},
			}
			if r.dry {
				planned, err := r.provider.Stacks().Plan(ctx, spec, progress)
				if err != nil {
					return err
				}
				r.dryRunPlan.apps[slot] = withReleaseMintedAtDeploy(planned, entry.Build.Release())
				return nil
			}
			r.recordProvisioning(entry.App)
			if err := r.recordAppStack(ctx, entry, provider.StackResult{}); err != nil {
				return err
			}
			result, err := r.provider.Stacks().Provision(ctx, spec, progress)
			if err != nil {
				return err
			}
			if err := r.recordAppStack(ctx, entry, result); err != nil {
				return err
			}
			if err := r.warmFunctions(ctx, result.Functions, progress); err != nil {
				return err
			}
			if err := r.embedBytecodeCaches(ctx, entry, result.Functions, progress); err != nil {
				return err
			}
			return r.recordStagedDeployment(ctx, entry, facts, images, values, result)
		})
	})
}

func (r *deployRun) recordAppStack(ctx context.Context, entry provider.AppEntry, result provider.StackResult) error {
	return stackrecords.Write(ctx, r.provider.KeyValues(), r.spec.Tier, r.spec.Slug, entry.Stack, stackrecords.Stack{
		Kind:       provider.StackApp,
		App:        entry.App,
		Release:    entry.Build.Release().String(),
		Build:      entry.Build.String(),
		Functions:  result.Functions,
		Containers: result.Containers,
		WrittenBy:  provider.WrittenByVersion(""),
	})
}

func (r *deployRun) refuseToAdopt(ctx context.Context, stack naming.StackName) error {
	inspectStack := r.provider.Hooks().InspectStack
	if inspectStack == nil {
		return nil
	}
	_, recorded, err := stackrecords.Read(ctx, r.provider.KeyValues(), r.spec.Tier, r.spec.Slug, stack)
	if err != nil || recorded {
		return err
	}
	inspected, err := inspectStack(ctx, r.ref(stack))
	if err != nil {
		return err
	}
	if !inspected.Present {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s already exists and this project has no record of it: ocel deploys over what it provisioned itself, never over what it finds. "+
			"Remove it, or deploy this project under another name",
		stack)
}

func (r *deployRun) appServing(entry provider.AppEntry) (AppServing, error) {
	return AppServingFor(AppServingInput{
		Root:              r.artifactRoot,
		Project:           naming.Sanitize(r.spec.Slug),
		App:               entry.App,
		Framework:         entry.Manifest.GetFramework().GetName(),
		Stack:             entry.Stack,
		Coordinate:        appCoordinate(r.spec, entry.App, entry.Build.Release()),
		EdgeRunsCode:      r.front.Facts().RunsCode,
		EdgeSignsForwards: r.router.Facts().SignsOriginForwards,
	})
}

func (r *deployRun) ref(stack naming.StackName) provider.StackRef {
	return provider.StackRef{Project: r.spec.Slug, Tier: r.spec.Tier, Name: stack}
}

func (r *deployRun) publishedBindings() *publishedBindings { return r.published }

func (r *deployRun) boundNamesUsedBy(app string) (map[string]bool, error) {
	resources, err := manifestResources(r.manifest)
	if err != nil {
		return nil, err
	}
	edges := map[string]bool{}
	for _, usage := range r.manifest.GetUsages() {
		if usage.GetApp() == app {
			edges[usage.GetResource()] = true
		}
	}
	names := map[string]bool{}
	for _, resource := range resources {
		if edges[resource.Name] {
			names[boundName(resource)] = true
		}
	}
	return names, nil
}

func (r *deployRun) grants(ctx context.Context, entry provider.AppEntry) ([]provider.Binding, error) {
	used, err := r.boundNamesUsedBy(entry.App)
	if err != nil {
		return nil, err
	}
	var grants []provider.Binding
	for _, binding := range r.bindings {
		if used[binding.Name] {
			grants = append(grants, binding)
		}
	}
	consumed, err := r.publishedBindings().Published(ctx)
	if err != nil {
		return nil, err
	}
	for _, binding := range consumed {
		if !used[binding.Name] {
			continue
		}
		if !slices.ContainsFunc(grants, func(grant provider.Binding) bool { return grant.Name == binding.Name }) {
			grants = append(grants, binding)
		}
	}
	slices.SortFunc(grants, func(a, b provider.Binding) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	if verifyGrants := r.provider.Hooks().VerifyGrants; verifyGrants != nil {
		for _, binding := range grants {
			if err := verifyGrants(ctx, binding); err != nil {
				return nil, err
			}
		}
	}
	return grants, nil
}

func (r *deployRun) appValues(ctx context.Context, entry provider.AppEntry, grants []provider.Binding) (provider.AppValues, error) {
	values, err := r.manifestValues(entry, grants)
	if err != nil {
		return provider.AppValues{}, err
	}
	values.ContainerEnv = r.containerEnv(entry, values)
	return values, nil
}

func (r *deployRun) manifestValues(entry provider.AppEntry, grants []provider.Binding) (provider.AppValues, error) {
	values := provider.AppValues{
		Plain:     map[string]string{},
		Sensitive: map[string]string{},
		Owners:    map[string]string{},
		Bindings:  grants,
		Folder:    entry.Manifest.GetFolder(),
		Phase:     r.spec.Phase,
	}
	for _, variable := range entry.Manifest.GetVariables() {
		switch variable.GetClass() {
		case resourcesv1.VariableClass_VARIABLE_CLASS_SECRET:
			values.Secrets = append(values.Secrets, provider.SecretRef{Key: variable.GetKey(), Folder: variable.GetFolder()})
		case resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE:
			values.Sensitive[variable.GetKey()] = variable.GetValue()
		case resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN:
			values.Plain[variable.GetKey()] = variable.GetValue()
		default:
			return provider.AppValues{}, refusal.Refuse(refusal.CodeInvalid,
				"%s declares %s with class %s, which this deploy cannot deliver to a function; declare it as `plain`, `sensitive` or `secret`",
				entry.App, variable.GetKey(), variable.GetClass())
		}
		values.Owners[variable.GetKey()] = entry.App
	}
	return values, nil
}

func (r *deployRun) functionSpecs(entry provider.AppEntry) []provider.FunctionSpec {
	var specs []provider.FunctionSpec
	for _, fn := range entry.Manifest.GetServerless().GetFunctions() {
		artifact, _ := r.artifact(fn.GetLogicalName())
		specs = append(specs, provider.FunctionSpec{
			Name:      fn.GetLogicalName(),
			Route:     fn.GetRouteId(),
			EntryFile: fn.GetEntryFile(),
			Framework: frameworkOf(fn),
			Artifact:  artifact,
			Image:     r.functionImage(fn.GetLogicalName()),
		})
	}
	return specs
}

func frameworksOf(manifest *contractv1.Manifest) []string {
	var frameworks []string
	for _, app := range manifest.GetApps() {
		if name := app.GetFramework().GetName(); name != "" && !slices.Contains(frameworks, name) {
			frameworks = append(frameworks, name)
		}
	}
	slices.Sort(frameworks)
	return frameworks
}

func entryLogicalName(app *contractv1.ManifestApp, entry string) string {
	if entry == "" {
		return ""
	}
	for _, fn := range app.GetServerless().GetFunctions() {
		if routeOf(fn) == entry {
			return fn.GetLogicalName()
		}
	}
	return ""
}

func (r *deployRun) embedBytecodeCaches(ctx context.Context, entry provider.AppEntry, functions []provider.Function, progress progress.Log) error {
	embedCode := r.provider.Hooks().EmbedCode
	if embedCode == nil {
		return nil
	}
	for _, fn := range functions {
		ref, ok := r.artifact(fn.Name)
		if !ok {
			continue
		}
		if err := embedCode(ctx, fn.Physical, ref, progress); err != nil {
			return fmt.Errorf("embed %s's bytecode cache for %s: %w", fn.Name, entry.App, err)
		}
	}
	return nil
}

func (r *deployRun) warmFunctions(ctx context.Context, functions []provider.Function, progress progress.Log) error {
	warmFunctions := r.provider.Hooks().WarmFunctions
	if warmFunctions == nil || len(functions) == 0 {
		return nil
	}
	targets := make([]string, 0, len(functions))
	for _, fn := range functions {
		if fn.Physical != "" {
			targets = append(targets, fn.Physical)
		}
	}
	return warmFunctions(ctx, targets, progress)
}

func declaredVariables(clientBundle bool, values provider.AppValues) []router.VariableRecord {
	names := make([]string, 0, len(values.Plain)+len(values.Sensitive)+len(values.Secrets))
	for _, key := range slices.Sorted(maps.Keys(values.Plain)) {
		if !processenv.IsInjected(clientBundle, key) {
			names = append(names, key)
		}
	}
	names = append(names, slices.Sorted(maps.Keys(values.Sensitive))...)
	folders := make(map[string]string, len(values.Secrets))
	for _, secret := range values.Secrets {
		names = append(names, secret.Key)
		folders[secret.Key] = secret.Folder
	}
	slices.Sort(names)
	declared := make([]router.VariableRecord, 0, len(names))
	for _, name := range names {
		declared = append(declared, router.VariableRecord{Key: name, Folder: folders[name]})
	}
	return declared
}

func (r *deployRun) recordStagedDeployment(ctx context.Context, entry provider.AppEntry, facts AppServing, images provider.ImagePushes, values provider.AppValues, result provider.StackResult) error {
	urlByLogical := make(map[string]string, len(result.Functions))
	physicalByLogical := make(map[string]string, len(result.Functions))
	for _, fn := range result.Functions {
		urlByLogical[fn.Name] = fn.URL
		physicalByLogical[fn.Name] = fn.Physical
	}
	urls := make(map[string]string, len(result.Functions))
	var logical []string
	for _, fn := range entry.Manifest.GetServerless().GetFunctions() {
		logical = append(logical, fn.GetLogicalName())
		if url := urlByLogical[fn.GetLogicalName()]; url != "" {
			urls[routeOf(fn)] = url
		}
	}
	coordinate := appCoordinate(r.spec, entry.App, entry.Build.Release())
	var routing any
	if facts.EdgeDispatch != nil {
		routing = json.RawMessage(facts.EdgeDispatch.Manifest)
	}
	record := router.DeploymentRecord{
		RoutingManifest:  routing,
		App:              entry.App,
		Framework:        entry.Manifest.GetFramework().GetName(),
		Build:            r.spec.Builds[entry.App],
		DeploymentID:     entry.Build.DeploymentID(),
		Entry:            facts.Entry,
		EntryFunction:    physicalByLogical[entryLogicalName(entry.Manifest, facts.Entry)],
		Image:            images.ImageRef(entry.App),
		Physical:         physicalOf(result.Containers, entry.App),
		Revisions:        revisionsOf(result, entry.App, logical),
		Origin:           originOf(result.Containers, entry.App),
		HealthPath:       entry.HealthCheckPath,
		FunctionURLs:     urls,
		AssetPrefix:      coordinate.AssetKey(""),
		IsrPrefix:        withoutSlash(coordinate.ISRPrefix()),
		IsrWriteSecret:   result.ISRWriteSecret,
		CreatedAt:        time.Now().Unix(),
		BuildFingerprint: entry.Build.Fingerprint(),
		Variables:        declaredVariables(entry.Manifest.GetClientBundle(), values),
		Needs:            r.needs[entry.App].Needs,
		SupportInEffect:  r.needs[entry.App].InEffect,
		Waived:           r.needs[entry.App].Waived,
	}
	code, err := r.edgeCode(entry, result)
	if err != nil {
		return err
	}
	if code != nil {
		record.EdgeWorkers = code
		record.Envelope = result.Envelope
		if env := variableEnv(values); len(env) > 0 {
			record.Env = env
		}
	}
	return r.ledger.putStaged(ctx, record)
}

func (r *deployRun) edgeCode(entry provider.AppEntry, result provider.StackResult) (*router.Code, error) {
	if result.EdgeBundleKey == "" {
		return nil, nil
	}
	compatibility := r.front.Facts().Compatibility
	if compatibility.IsZero() {
		return nil, nil
	}
	bundle, err := os.ReadFile(filepath.Join(buildoutput.AppRoot(r.artifactRoot, entry.App), filepath.FromSlash(edge.AppBundleFile)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"%s was released with an edge bundle at %s but its build left no %s for the edge to load; rebuild the app",
			entry.App, result.EdgeBundleKey, edge.AppBundleFile)
	}
	if err != nil {
		return nil, fmt.Errorf("read the edge bundle %s runs: %w", entry.App, err)
	}
	return &router.Code{
		BundleKey:   result.EdgeBundleKey,
		ID:          loaderID(bundle, compatibility.Date, compatibility.Flags),
		CompatDate:  compatibility.Date,
		CompatFlags: compatibility.Flags,
	}, nil
}

func variableEnv(values provider.AppValues) map[string]string {
	env := make(map[string]string, len(values.Plain)+1)
	maps.Copy(env, values.Plain)
	if values.Folder != "" {
		env[processenv.AppFolderEnvVar] = values.Folder
	}
	return env
}

func loaderID(bundle []byte, compatDate string, compatFlags []string) string {
	sum := sha256.New()
	sum.Write(bundle)
	sum.Write([]byte(compatDate))
	sum.Write([]byte(strings.Join(compatFlags, ",")))
	return hex.EncodeToString(sum.Sum(nil))
}

func (r *deployRun) promote(ctx context.Context) (*progressv1.OperationEvent, error) {
	if r.dry {
		r.dryRunPlan.promotion = r.planPromotionGroup()
		r.sender.send(planEvent(r.dryRunPlanProto()))
		return okResult(), nil
	}
	propagation := r.router.Facts().Propagation
	promotion := router.Promotion{
		PromotionID: r.spec.PromotionID,
		Ts:          time.Now().Unix(),
		Builds:      r.spec.Builds,
		Tag:         r.spec.Tag,
		Propagation: &propagation,
	}
	if err := r.spanEvents.run(r.spans.Promotion, func(u *spanRun) error {
		return u.phase(func(progress progress.Log) error {
			if err := r.publishInlineBindings(ctx); err != nil {
				r.restoreInlineBindings(ctx, progress)
				return err
			}
			dropped, err := r.sharedStack.promote(ctx, promoteRequest{pointer: r.spec.Pointer, replaces: r.replaces, promotion: promotion}, progress)
			if err != nil {
				r.restoreInlineBindings(ctx, progress)
				return errors.Join(err, r.reclaimDropped(ctx, r.spec.Pointer, dropped, progress))
			}
			if err := r.checkpoint(ctx); err != nil {
				return err
			}
			if err := r.reclaimDropped(ctx, r.spec.Pointer, dropped, progress); err != nil {
				progress.Warn(unreclaimedWarning(promotion.PromotionID, err))
			}
			r.pruneInlineBindings(ctx, progress)
			if r.spec.Tier != environment.TierPreview {
				return nil
			}
			return stackrecords.RecordEnvironmentMeta(ctx, r.provider.KeyValues(),
				r.spec.Tier, r.spec.Slug, r.spec.Env, r.spec.Label)
		})
	}); err != nil {
		return nil, err
	}
	return r.result(promotion, propagation)
}

func (r *deployRun) result(promotion router.Promotion, propagation router.Propagation) (*progressv1.OperationEvent, error) {
	result := &progressv1.OperationResult{
		Success:     true,
		PromotionId: promotion.PromotionID,
		Propagation: propagationProto(&propagation),
	}
	r.reportApps(result)
	for slot, hosts := range r.servedHostnames() {
		for _, host := range hosts {
			if r.hostingMode() == hostingProduction && !r.state.Ready(host, r.front.Kind(), r.readAppRouter(r.spec.Apps[slot].App)) {
				continue
			}
			r.outcomes[slot].Urls = append(r.outcomes[slot].Urls, "https://"+host)
		}
	}
	result.UrlNotes = r.pending
	return &progressv1.OperationEvent{Body: &progressv1.OperationEvent_Result{Result: result}}, nil
}

func (r *deployRun) publish(ctx context.Context, bindings []provider.Binding) error {
	publishing := make([]variablestore.NamedBindingWrite, 0, len(bindings))
	for _, binding := range bindings {
		message, err := provider.BindingMessage(binding)
		if err != nil {
			return err
		}
		if err := variablestoreserver.VerifyGrantScope(message); err != nil {
			return connect.NewError(connect.CodeInvalidArgument, err)
		}
		pair, err := variablestoreserver.BindingPair(variablestore.OwnerOcel, message)
		if err != nil {
			return err
		}
		publishing = append(publishing, variablestore.NamedBindingWrite{Name: binding.Name, Write: pair})
	}
	if _, err := r.values.SetBindings(ctx, r.scope, bindingEnvironment(r.spec), variablestore.OwnerOcel, publishing); err != nil {
		return fmt.Errorf("publish %s's bindings: %w", r.scope.Project, err)
	}
	if err := r.pruneBindings(ctx, bindings); err != nil {
		return err
	}
	r.publishedBindings().forget()
	return nil
}

func (r *deployRun) pruneBindings(ctx context.Context, bindings []provider.Binding) error {
	environment := bindingEnvironment(r.spec)
	published, err := r.values.ListBindings(ctx, r.scope, environment)
	if err != nil {
		return fmt.Errorf("read %s's published bindings: %w", r.scope.Project, err)
	}
	stale := map[string]int64{}
	for _, record := range published {
		if record.Owner != variablestore.OwnerOcel || record.Environment != environment {
			continue
		}
		if slices.ContainsFunc(bindings, func(binding provider.Binding) bool { return binding.Name == record.Name }) {
			continue
		}
		stale[record.Name] = record.Version
	}
	if len(stale) == 0 {
		return nil
	}
	if _, err := r.values.RemoveUnchangedBindings(ctx, r.scope, environment, stale); err != nil {
		return fmt.Errorf("prune %s's published bindings: %w", r.scope.Project, err)
	}
	return nil
}

type publishedBindings struct {
	store       variablestore.Store
	scope       variablestore.Scope
	environment string

	mu       sync.Mutex
	carried  []provider.Binding
	resolved []provider.Binding
	loaded   bool
}

func (p *publishedBindings) carry(bindings []provider.Binding) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.carried = bindings
	p.resolved, p.loaded = nil, false
}

func (p *publishedBindings) forget() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resolved, p.loaded = nil, false
}

func (p *publishedBindings) Published(ctx context.Context) ([]provider.Binding, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.loaded {
		return p.resolved, nil
	}
	names, err := p.store.PublishedNames(ctx, p.scope, p.environment)
	if err != nil {
		return nil, err
	}
	resolved, err := p.store.ResolveBindings(ctx, p.scope, p.environment, names)
	if err != nil {
		return nil, err
	}
	bindings := slices.Clone(p.carried)
	for i, published := range resolved {
		if slices.ContainsFunc(p.carried, func(carried provider.Binding) bool { return carried.Name == names[i] }) {
			continue
		}
		binding, err := bindingPublished(names[i], published)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	slices.SortFunc(bindings, func(a, b provider.Binding) int { return strings.Compare(a.Name, b.Name) })
	p.resolved, p.loaded = bindings, true
	return bindings, nil
}

func (p *publishedBindings) Names(ctx context.Context) ([]string, error) {
	bindings, err := p.Published(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		names = append(names, binding.Name)
	}
	return names, nil
}

func (p *publishedBindings) Named(ctx context.Context, name string) (provider.Binding, error) {
	bindings, err := p.Published(ctx)
	if err != nil {
		return provider.Binding{}, err
	}
	for _, binding := range bindings {
		if binding.Name == name {
			return binding, nil
		}
	}
	published, err := p.store.ResolveBinding(ctx, p.scope, p.environment, name)
	if err != nil {
		return provider.Binding{}, err
	}
	return bindingPublished(name, published)
}

func bindingPublished(name string, published variablestore.StoredBinding) (provider.Binding, error) {
	message, err := variablestoreserver.DecodeBinding(published.Value)
	if err != nil {
		return provider.Binding{}, fmt.Errorf("read binding %s: %w", name, err)
	}
	binding := provider.BindingOf(message)
	binding.Version = published.Version
	return binding, nil
}

func revisionsOf(result provider.StackResult, app string, logical []string) map[string]string {
	revisions := make(map[string]string, len(logical)+1)
	for _, container := range result.Containers {
		if container.Name == app && container.Physical != "" && container.Revision != "" {
			revisions[container.Physical] = container.Revision
		}
	}
	for _, fn := range result.Functions {
		if slices.Contains(logical, fn.Name) && fn.Physical != "" && fn.Revision != "" {
			revisions[fn.Physical] = fn.Revision
		}
	}
	if len(revisions) == 0 {
		return nil
	}
	return revisions
}

func physicalOf(containers []provider.AppContainer, app string) string {
	for _, container := range containers {
		if container.Name == app {
			return container.Physical
		}
	}
	return ""
}

func originOf(containers []provider.AppContainer, app string) string {
	for _, container := range containers {
		if container.Name == app {
			return container.URL
		}
	}
	return ""
}

func imageToRun(images provider.ImagePushes, entry provider.AppEntry) string {
	if pushed := images.ImageRef(entry.App); pushed != "" {
		return pushed
	}
	return entry.Image
}

func unseen(hosts []string, seen map[string]bool) []string {
	var out []string
	for _, host := range hosts {
		if seen[host] {
			continue
		}
		seen[host] = true
		out = append(out, host)
	}
	return out
}

func frameworkOf(fn *contractv1.ManifestFunction) buildoutput.Framework {
	return buildoutput.Framework{Name: fn.GetFramework().GetName(), Arch: fn.GetFramework().GetArch()}
}
