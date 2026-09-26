package providerserver

import (
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

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/providerkit/envvars"
	"github.com/ocelhq/ocel/pkg/providerkit/envvarsserver"
	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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

type deployStages struct {
	Environment Stage
	Infra       Stage
	Apps        map[string]Stage
	Edge        Stage
	Hostnames   Stage
	Promotion   Stage
	Roster      []Stage
}

func newDeployStages(plan provider.DeployPlan) deployStages {
	s := deployStages{
		Environment: UnitStage(naming.UnitEnvironment, environmentUnitTitle, progressv1.Phase_PHASE_PROVISIONING),
		Infra:       UnitStage(plan.Infra.String(), infraUnitTitle, progressv1.Phase_PHASE_PROVISIONING),
		Edge:        UnitStage(naming.UnitEdge, edgeUnitTitle, progressv1.Phase_PHASE_PROVISIONING),
		Hostnames:   UnitStage(naming.UnitHostnames, hostnamesUnitTitle, progressv1.Phase_PHASE_PROVISIONING),
		Promotion:   UnitStage(naming.UnitPromotion, promotionUnitTitle, progressv1.Phase_PHASE_FINALIZING),
		Apps:        make(map[string]Stage, len(plan.Apps)),
	}
	s.Roster = append(s.Roster, s.Environment)
	if !plan.Infra.IsZero() {
		s.Roster = append(s.Roster, s.Infra)
	}
	for _, entry := range plan.Apps {
		app := UnitStage(entry.Stack.String(), entry.App, progressv1.Phase_PHASE_PROVISIONING)
		s.Apps[entry.App] = app
		s.Roster = append(s.Roster, app)
	}
	s.Roster = append(s.Roster, s.Edge)
	if plan.Class != edge.ClassPreview {
		s.Roster = append(s.Roster, s.Hostnames)
	}
	s.Roster = append(s.Roster, s.Promotion)
	return s
}

type deployRun struct {
	*stackSession
	gate       Gate
	features   []string
	transforms []string
	sender     *eventStream
	tracked    *stageScope
	manifest   *contractv1.Manifest
	plan       provider.DeployPlan
	stages     deployStages

	wildcard   stackrecords.Wildcard
	previewOn  string
	selection  *contractv1.EdgeSelection
	configured []ConfiguredHost
	pending    []string

	values    envvars.Store
	scope     envvars.Scope
	published *publishedBindings

	registry images.Registry
	images   images.Store

	dry           bool
	draft         draft
	allowDegraded []string

	outcomes []*progressv1.AppResult

	mu             sync.Mutex
	artifacts      map[string]provider.ArtifactRef
	functionImages map[string]string
	needs          NeedRecords
	bindings       []provider.Binding
	functions      map[string][]provider.Function
}

func (r *deployRun) recordArtifact(logical string, ref provider.ArtifactRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.artifacts[logical] = ref
}

func (r *deployRun) artifact(logical string) (provider.ArtifactRef, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, held := r.artifacts[logical]
	return ref, held
}

func (r *deployRun) recordFunctions(app string, functions []provider.Function) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.functions[app] = functions
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
	plan, err := buildDeployPlan(req, promotionID)
	if err != nil {
		return nil, err
	}
	front, err := h.edgeFor(p, req.GetEdge())
	if err != nil {
		return nil, err
	}
	features, err := bootstrapplan.RequiredFeatures(gate.Bootstrap.Catalogue(), frameworksOf(req.GetManifest()), string(gate.Edge))
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	run := &deployRun{
		stackSession: &stackSession{
			provider: p,
			front:    front,
			store:    stackStore{records: p.Records(), name: stackrecords.EdgeStackRecord(plan.Class, plan.Slug)},
		},
		gate:           gate,
		features:       features,
		transforms:     h.session.transforms(),
		sender:         sender,
		tracked:        newStageScope(sender),
		manifest:       req.GetManifest(),
		plan:           plan,
		selection:      req.GetEdge(),
		values:         envvars.Store{Records: p.Records(), Cipher: p.Cipher()},
		scope:          envvars.Scope{Project: plan.Slug, Class: plan.Class},
		artifacts:      map[string]provider.ArtifactRef{},
		functionImages: map[string]string{},
		functions:      map[string][]provider.Function{},

		dry:           req.GetDry(),
		allowDegraded: req.GetEdge().GetAllowDegraded(),
	}
	if err := run.openImages(ctx, req.GetImageRegistry()); err != nil {
		return nil, err
	}
	run.published = &publishedBindings{store: run.values, scope: run.scope, environment: bindingEnvironment(plan)}
	if run.state, err = run.store.read(ctx); err != nil {
		return nil, err
	}
	run.stages = newDeployStages(plan)
	run.outcomes = pendingOutcomes(plan.Apps)
	run.draft.apps = make([]provider.Plan, len(plan.Apps))
	run.tracked.declare(run.stages.Roster...)
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

func (r *deployRun) reportApps(result *progressv1.ResultEvent) {
	result.Apps = r.outcomes
}

func (r *deployRun) execute(ctx context.Context) (*progressv1.OperationEvent, error) {
	if err := r.tracked.unit(r.stages.Environment, func(env *unitRun) error {
		return env.phase(progressv1.Phase_PHASE_PROVISIONING, func(progress edge.Progress) error {
			return r.admission(ctx, progress)
		})
	}); err != nil {
		return nil, err
	}
	if err := r.raiseEdge(ctx); err != nil {
		return nil, err
	}
	if err := r.provision(ctx); err != nil {
		return nil, err
	}
	if err := r.settleHostnames(ctx); err != nil {
		return nil, err
	}
	return r.promote(ctx)
}

func (r *deployRun) admission(ctx context.Context, progress edge.Progress) error {
	if err := r.admit(ctx, progress); err != nil {
		return err
	}
	if err := r.admitDomains(ctx); err != nil {
		return err
	}
	if err := r.admitBindings(ctx, progress); err != nil {
		return err
	}
	if !r.dry {
		if err := r.rememberProject(ctx); err != nil {
			return err
		}
	}
	if err := r.checkNeeds(ctx); err != nil {
		return err
	}
	return r.preflight(ctx, progress)
}

func (r *deployRun) admit(ctx context.Context, progress edge.Progress) error {
	_, err := r.gate.Admit(ctx, r.plan.Class, r.features, !r.dry, progress)
	return err
}

func (r *deployRun) admitDomains(ctx context.Context) error {
	hosts := r.hostnames()
	if r.plan.Class == edge.ClassPreview {
		wildcard, err := stackrecords.ReadWildcard(ctx, r.provider.Records())
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
		if r.front.Facts().AddressesItself {
			return nil
		}
		return refusal.Refuse(refusal.CodeNotReady,
			"no domains.production declared on the project or any app, so this deploy has nowhere to serve: declare one, and the deploy that reads it settles it")
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
	r.installSettler(writer, r.selection.GetDns().GetZone())
	r.settle.owed = unattended(r.sender)
	return nil
}

func (r *deployRun) rememberProject(ctx context.Context) error {
	name := stackrecords.ProjectRecord(r.plan.Class, r.plan.Slug)
	held, err := records.ReadOrEmpty(ctx, r.provider.Records(), name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	if held.Bytes, err = json.Marshal(stackrecords.Project{Features: r.features}); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	if _, err := r.provider.Records().Write(ctx, held); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return nil
}

type hostingWorld int

const (
	hostingProduction hostingWorld = iota
	hostingGlobalPreview
	hostingProjectPreview
)

func (r *deployRun) world() hostingWorld {
	if r.plan.Class != edge.ClassPreview {
		return hostingProduction
	}
	if r.wildcard.BaseDomain != "" && len(r.hostnames()) == 0 {
		return hostingGlobalPreview
	}
	return hostingProjectPreview
}

func (r *deployRun) raiseEdge(ctx context.Context) error {
	return r.tracked.unit(r.stages.Edge, func(u *unitRun) error {
		return u.phase(progressv1.Phase_PHASE_PROVISIONING, func(progress edge.Progress) error {
			if r.dry {
				progress.Say(fmt.Sprintf("Reading the %s edge", r.front.Kind()))
				r.draft.edge = r.drawEdge()
				return nil
			}
			progress.Say(fmt.Sprintf("Reconciling the %s edge", r.front.Kind()))
			return r.reconcileEdge(ctx)
		})
	})
}

func (r *deployRun) reconcileEdge(ctx context.Context) error {
	spec := edge.StackSpec{
		Version:     stackVersion,
		Class:       r.plan.Class,
		Slug:        r.plan.Slug,
		PruneRoutes: true,
	}
	var base string
	switch r.world() {
	case hostingProduction:
		spec.Domains = r.hostnames()
		spec.DomainApps = r.domainApps()
	case hostingGlobalPreview:
		spec.PruneOnly = true
	default:
		if len(r.plan.Apps) == 0 {
			spec.PruneOnly = true
			break
		}
		base = r.previewOn
		spec.Domains = []string{edge.PreviewWildcard(base)}
	}
	program, err := edgeProgramFor(ctx, r.provider, r.front, provider.EdgeProgramRequest{
		Class:             r.plan.Class,
		Slug:              r.plan.Slug,
		Env:               r.plan.Env,
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
	r.stack = stack
	return r.checkpoint(ctx)
}

func (r *deployRun) settleHostnames(ctx context.Context) error {
	if r.dry || r.world() != hostingProduction {
		return nil
	}
	return r.tracked.unit(r.stages.Hostnames, func(u *unitRun) error {
		return u.phase(progressv1.Phase_PHASE_PROVISIONING, func(progress edge.Progress) error {
			settling := &hostnames{stackSession: r.stackSession}
			for _, host := range r.configured {
				serving := r.state.Host(host.Hostname).Serving()
				if serving == r.front.Kind() {
					continue
				}
				if serving != "" {
					r.pending = append(r.pending, fmt.Sprintf(
						"%s is still served by the %s edge, not the %s edge this deploy promoted to: `ocel domain add` moves it, in the order that keeps it answering",
						host.Hostname, serving, r.front.Kind()))
					continue
				}
				_, err := settling.settleHost(ctx, host, progress)
				if waits, held := provider.LeftPending(err); held {
					r.pending = append(r.pending, fmt.Sprintf("%s is not served yet: %s", host.Hostname, waits))
					continue
				}
				if err != nil {
					return err
				}
			}
			return nil
		})
	})
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
	if r.world() != hostingGlobalPreview {
		return ""
	}
	return r.wildcard.BaseDomain
}

func (r *deployRun) appNames() []string {
	names := make([]string, 0, len(r.plan.Apps))
	for _, entry := range r.plan.Apps {
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
		name := strings.ToLower(strings.TrimSpace(r.plan.Apps[slot].App))
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
	tier := environmentTier(r.plan.Class)
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
	if r.world() == hostingGlobalPreview {
		return edge.SharedPreview(r.plan.Slug, r.previewOn)
	}
	return edge.ProjectPreview(r.previewOn)
}

func (r *deployRun) previewLabel(slot int) string {
	if r.world() != hostingGlobalPreview || !r.front.Facts().RoutesPreviewsByLabel {
		return ""
	}
	return r.previewSite().Label(r.plan.Pointer, edge.AppAt(r.appNames(), slot))
}

func (r *deployRun) servedHostnames() [][]string {
	if r.plan.Class == edge.ClassPreview {
		served := make([][]string, len(r.plan.Apps))
		site, names := r.previewSite(), r.appNames()
		for slot := range served {
			if host := site.Host(r.plan.Pointer, edge.AppAt(names, slot)); host != "" {
				served[slot] = []string{host}
			}
		}
		return served
	}
	tier := environmentTier(r.plan.Class)
	own := make([][]string, len(r.plan.Apps))
	for slot, entry := range r.plan.Apps {
		own[slot] = tierHostnames(entry.Manifest.GetDomains(), tier)
	}
	return appbuild.AttributeHostnames(tierHostnames(r.manifest.GetDomains(), tier), own)
}

func (r *deployRun) checkpoint(ctx context.Context) error {
	r.state.Kind = r.front.Kind()
	r.state.Edge = r.stack.State()
	if r.plan.Class == edge.ClassPreview {
		r.state.Edge.GlobalPreview = r.globalPreview()
	}
	return r.store.write(ctx, r.state)
}

func (r *deployRun) checkNeeds(ctx context.Context) error {
	check := NeedCheck{
		Edge:          r.front,
		Root:          appbuild.ArtifactRoot(),
		AllowDegraded: r.allowDegraded,
		Degraded: func(need edge.Need, detail string) {
			r.sender.send(degradedEvent(need, detail))
		},
	}
	records, err := check.Run(ctx, r.manifest)
	if err != nil {
		return err
	}
	r.needs = records
	return nil
}

func (r *deployRun) preflight(ctx context.Context, progress edge.Progress) error {
	resources, err := manifestResources(r.manifest)
	if err != nil {
		return err
	}
	grants, err := r.reader().Published(ctx)
	if err != nil {
		return err
	}
	if err := RefuseUnreachableBindings(r.provider.Facts().Vendor, r.provider.Facts().Bindings, proxied, resources, grants); err != nil {
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
		Plan:      r.plan,
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
	apps := make([]provider.AppUsage, 0, len(r.plan.Apps))
	for _, entry := range r.plan.Apps {
		used, err := r.used(entry.App)
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
	failures := make([]error, len(r.plan.Apps))
	var apps errgroup.Group
	apps.SetLimit(appConcurrency)
	for slot, entry := range r.plan.Apps {
		apps.Go(func() error {
			failures[slot] = r.provisionApp(ctx, slot, entry)
			return nil
		})
	}
	_ = apps.Wait()
	var first error
	for slot, err := range failures {
		r.outcomes[slot] = appOutcome(r.plan.Apps[slot].App, err)
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
	if r.plan.Infra.IsZero() {
		return nil
	}
	resources, err := manifestResources(r.manifest)
	if err != nil {
		return err
	}
	return r.tracked.unit(r.stages.Infra, func(u *unitRun) error {
		return u.phase(progressv1.Phase_PHASE_PROVISIONING, func(progress edge.Progress) error {
			if err := r.refuseToAdopt(ctx, r.plan.Infra); err != nil {
				return err
			}
			stack := provider.StackPlan{
				Ref:       r.ref(r.plan.Infra),
				Kind:      provider.StackInfra,
				Edge:      r.front,
				Tags:      infraTags(r.plan),
				Resources: resources,
				Bindings:  r.reader(),
			}
			if r.dry {
				progress.Say("Planning the environment's infrastructure")
				drawn, err := r.provider.Stacks().Plan(ctx, stack, progress)
				if err != nil {
					return err
				}
				r.draft.infra = drawn
				r.draft.parameters, err = r.drawValues(ctx)
				return err
			}
			progress.Say("Provisioning the environment's infrastructure")
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
			return stackrecords.Write(ctx, r.provider.Records(), r.plan.Class, r.plan.Slug, r.plan.Infra, stackrecords.Stack{
				Kind:      provider.StackInfra,
				Bindings:  result.Bindings,
				WrittenBy: provider.WrittenByVersion(""),
			})
		})
	})
}

func (r *deployRun) provisionApp(ctx context.Context, slot int, entry provider.AppEntry) error {
	return r.tracked.unit(r.stages.Apps[entry.App], func(u *unitRun) error {
		return u.phase(progressv1.Phase_PHASE_PROVISIONING, func(progress edge.Progress) error {
			if err := r.refuseToAdopt(ctx, entry.Stack); err != nil {
				return err
			}
			if r.dry {
				progress.Say("Planning " + entry.App)
			} else {
				progress.Say("Provisioning " + entry.App)
			}
			grants, err := r.grants(ctx, entry)
			if err != nil {
				return err
			}
			facts, err := r.serving(entry)
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
			staged, functions, err := r.stageFunctions(ctx, entry, pack, facts.Routing)
			if err != nil {
				return err
			}
			defer discardStaged(staged)
			images, err := r.imagePlan(ctx, entry, functions)
			if err != nil {
				return err
			}
			plan := provider.StackPlan{
				Ref:      r.ref(entry.Stack),
				Kind:     provider.StackApp,
				Edge:     r.front,
				Tags:     appTags(r.plan, entry),
				Uploads:  staged,
				Images:   images,
				Bindings: r.reader(),
				App: &provider.AppPlan{
					App:             entry.App,
					Framework:       entry.Manifest.GetFramework().GetName(),
					Entry:           entryLogicalName(r.manifest, entry.App, facts.Entry),
					Deployment:      entry.Build.DeploymentID(),
					Compute:         entry.Compute(),
					Functions:       r.functionSpecs(entry),
					Image:           runs(images, entry),
					HealthCheckPath: entry.HealthCheckPath,
					Arch:            entry.Arch,
					Values:          values,
					Grants:          grants,
					Routing:         facts.Routing,
					ISR:             facts.ISR,
					Bytecode:        facts.Bytecode,
					AssetPrefix:     facts.AssetPrefix,
					PreviewLabel:    r.previewLabel(slot),
					Guard:           facts.Guard,
					Packed:          pack.Packed,
					Proxied:         anyProxied(proxied, grants),
				},
			}
			if r.dry {
				drawn, err := r.provider.Stacks().Plan(ctx, plan, progress)
				if err != nil {
					return err
				}
				r.draft.apps[slot] = drawn
				return nil
			}
			result, err := r.provider.Stacks().Provision(ctx, plan, progress)
			if err != nil {
				return err
			}
			r.recordFunctions(entry.App, result.Functions)
			if err := r.warm(ctx, result.Functions, progress); err != nil {
				return err
			}
			if err := r.embed(ctx, entry, result.Functions, progress); err != nil {
				return err
			}
			if err := r.stage(ctx, entry, facts, images, values, result); err != nil {
				return err
			}
			return stackrecords.Write(ctx, r.provider.Records(), r.plan.Class, r.plan.Slug, entry.Stack, stackrecords.Stack{
				Kind:       provider.StackApp,
				App:        entry.App,
				Release:    entry.Build.Release().String(),
				Identity:   entry.Build.String(),
				Functions:  result.Functions,
				Containers: result.Containers,
				WrittenBy:  provider.WrittenByVersion(""),
			})
		})
	})
}

func (r *deployRun) refuseToAdopt(ctx context.Context, stack naming.StackName) error {
	inspectStack := r.provider.Hooks().InspectStack
	if inspectStack == nil {
		return nil
	}
	_, recorded, err := stackrecords.Read(ctx, r.provider.Records(), r.plan.Class, r.plan.Slug, stack)
	if err != nil || recorded {
		return err
	}
	state, err := inspectStack(ctx, r.ref(stack))
	if err != nil {
		return err
	}
	if !state.Present {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s is already standing and this project has no record of it: ocel deploys over what it stood up itself, never over what it finds. "+
			"Remove it, or deploy this project under another name",
		stack)
}

func (r *deployRun) serving(entry provider.AppEntry) (ServingFacts, error) {
	return ServingFactsFor(ServingQuery{
		Root:              appbuild.ArtifactRoot(),
		Project:           naming.Sanitize(r.plan.Slug),
		App:               entry.App,
		Framework:         entry.Manifest.GetFramework().GetName(),
		Stack:             entry.Stack,
		Coordinate:        appCoordinate(r.plan, entry.App, entry.Build.Release()),
		EdgeRunsCode:      r.front.Facts().RunsCode,
		EdgeSignsForwards: r.front.Facts().SignsOriginForwards,
	})
}

func (r *deployRun) ref(stack naming.StackName) provider.StackRef {
	return provider.StackRef{Project: r.plan.Slug, Class: r.plan.Class, Name: stack}
}

func (r *deployRun) reader() *publishedBindings { return r.published }

func (r *deployRun) used(app string) (map[string]bool, error) {
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
	used, err := r.used(entry.App)
	if err != nil {
		return nil, err
	}
	var grants []provider.Binding
	for _, binding := range r.bindings {
		if used[binding.Name] {
			grants = append(grants, binding)
		}
	}
	consumed, err := r.reader().Published(ctx)
	if err != nil {
		return nil, err
	}
	for _, binding := range consumed {
		if !used[binding.Name] {
			continue
		}
		at := slices.IndexFunc(grants, func(held provider.Binding) bool { return held.Name == binding.Name })
		if at < 0 {
			grants = append(grants, binding)
			continue
		}
		if len(grants[at].Wire) == 0 {
			grants[at].Wire = binding.Wire
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
	held, err := r.manifestValues(entry, grants)
	if err != nil {
		return provider.AppValues{}, err
	}
	held.Delivered = r.deliver(entry, held)
	return held, nil
}

func (r *deployRun) manifestValues(entry provider.AppEntry, grants []provider.Binding) (provider.AppValues, error) {
	held := provider.AppValues{
		Plain:     map[string]string{},
		Sensitive: map[string]string{},
		Owners:    map[string]string{},
		Bindings:  grants,
		Folder:    entry.Manifest.GetFolder(),
		Phase:     r.plan.Phase,
	}
	for _, variable := range entry.Manifest.GetVariables() {
		switch variable.GetClass() {
		case resourcesv1.VariableClass_VARIABLE_CLASS_SECRET:
			held.Secrets = append(held.Secrets, provider.SecretRef{Key: variable.GetKey(), Folder: variable.GetFolder()})
		case resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE:
			held.Sensitive[variable.GetKey()] = variable.GetValue()
		case resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN:
			held.Plain[variable.GetKey()] = variable.GetValue()
		default:
			return provider.AppValues{}, refusal.Refuse(refusal.CodeInvalid,
				"%s declares %s with class %s, which this deploy cannot deliver to a function; declare it as `plain`, `sensitive` or `secret`",
				entry.App, variable.GetKey(), variable.GetClass())
		}
		held.Owners[variable.GetKey()] = entry.App
	}
	return held, nil
}

func (r *deployRun) functionSpecs(entry provider.AppEntry) []provider.FunctionSpec {
	var specs []provider.FunctionSpec
	for _, fn := range r.manifest.GetFunctions() {
		if fn.GetApp() != entry.App {
			continue
		}
		artifact, _ := r.artifact(fn.GetLogicalName())
		specs = append(specs, provider.FunctionSpec{
			Name:      fn.GetLogicalName(),
			Route:     fn.GetRouteId(),
			Handler:   fn.GetHandler(),
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

func entryLogicalName(manifest *contractv1.Manifest, app, entry string) string {
	if entry == "" {
		return ""
	}
	for _, fn := range manifest.GetFunctions() {
		if fn.GetApp() == app && routeOf(fn) == entry {
			return fn.GetLogicalName()
		}
	}
	return ""
}

func (r *deployRun) embed(ctx context.Context, entry provider.AppEntry, functions []provider.Function, progress edge.Progress) error {
	embedCode := r.provider.Hooks().EmbedCode
	if embedCode == nil {
		return nil
	}
	for _, fn := range functions {
		ref, held := r.artifact(fn.Name)
		if !held {
			continue
		}
		if err := embedCode(ctx, fn.Physical, ref, progress); err != nil {
			return fmt.Errorf("embed %s's bytecode cache for %s: %w", fn.Name, entry.App, err)
		}
	}
	return nil
}

func (r *deployRun) warm(ctx context.Context, functions []provider.Function, progress edge.Progress) error {
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

func declaredVariables(clientBundle bool, held provider.AppValues) []edge.VariableRecord {
	names := make([]string, 0, len(held.Plain)+len(held.Sensitive)+len(held.Secrets))
	for _, key := range slices.Sorted(maps.Keys(held.Plain)) {
		if !appbuild.IsOcelInjectedEnv(clientBundle, key) {
			names = append(names, key)
		}
	}
	names = append(names, slices.Sorted(maps.Keys(held.Sensitive))...)
	folders := make(map[string]string, len(held.Secrets))
	for _, secret := range held.Secrets {
		names = append(names, secret.Key)
		folders[secret.Key] = secret.Folder
	}
	slices.Sort(names)
	declared := make([]edge.VariableRecord, 0, len(names))
	for _, name := range names {
		declared = append(declared, edge.VariableRecord{Key: name, Folder: folders[name]})
	}
	return declared
}

func (r *deployRun) stage(ctx context.Context, entry provider.AppEntry, facts ServingFacts, images provider.ImagePushes, values provider.AppValues, result provider.StackResult) error {
	urlByLogical := make(map[string]string, len(result.Functions))
	physicalByLogical := make(map[string]string, len(result.Functions))
	for _, fn := range result.Functions {
		urlByLogical[fn.Name] = fn.URL
		physicalByLogical[fn.Name] = fn.Physical
	}
	urls := make(map[string]string, len(result.Functions))
	var logical []string
	for _, fn := range r.manifest.GetFunctions() {
		if fn.GetApp() != entry.App {
			continue
		}
		logical = append(logical, fn.GetLogicalName())
		if url := urlByLogical[fn.GetLogicalName()]; url != "" {
			urls[routeOf(fn)] = url
		}
	}
	coordinate := appCoordinate(r.plan, entry.App, entry.Build.Release())
	var routing any
	if facts.EdgeRouting != nil {
		routing = json.RawMessage(facts.EdgeRouting.Manifest)
	}
	record := edge.DeploymentRecord{
		RoutingManifest:  routing,
		App:              entry.App,
		Framework:        entry.Manifest.GetFramework().GetName(),
		Identity:         r.plan.Builds[entry.App],
		DeploymentID:     entry.Build.DeploymentID(),
		Entry:            facts.Entry,
		EntryFunction:    physicalByLogical[entryLogicalName(r.manifest, entry.App, facts.Entry)],
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
		ValueFingerprint: entry.Build.Fingerprint(),
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
	return r.stack.Ledger().PutStaged(ctx, record)
}

func (r *deployRun) edgeCode(entry provider.AppEntry, result provider.StackResult) (*edge.Code, error) {
	if result.EdgeBundleKey == "" {
		return nil, nil
	}
	compatibility := r.front.Facts().Compatibility
	if compatibility.IsZero() {
		return nil, nil
	}
	bundle, err := os.ReadFile(filepath.Join(appbuild.AppArtifactRoot(appbuild.ArtifactRoot(), entry.App), filepath.FromSlash(edge.AppBundleFile)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"%s was released with an edge bundle at %s but its build left no %s for the edge to load; rebuild the app",
			entry.App, result.EdgeBundleKey, edge.AppBundleFile)
	}
	if err != nil {
		return nil, fmt.Errorf("read the edge bundle %s runs: %w", entry.App, err)
	}
	return &edge.Code{
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
		env[constants.AppFolderEnvName] = values.Folder
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
		r.draft.promotion = r.drawPromotion()
		r.sender.send(planEvent(r.drawn()))
		return okResult(), nil
	}
	flip := r.front.Facts().FlipBound
	promotion := edge.Promotion{
		PromotionID: r.plan.PromotionID,
		Ts:          time.Now().Unix(),
		Builds:      r.plan.Builds,
		Tag:         r.plan.Tag,
		Flip:        &flip,
	}
	if err := r.tracked.unit(r.stages.Promotion, func(u *unitRun) error {
		return u.phase(progressv1.Phase_PHASE_FINALIZING, func(progress edge.Progress) error {
			progress.Say("Promoting the deployment")
			if err := r.stack.Promote(ctx, promotion, r.plan.Pointer, progress); err != nil {
				return err
			}
			if err := r.checkpoint(ctx); err != nil {
				return err
			}
			if r.plan.Class != edge.ClassPreview {
				return nil
			}
			return stackrecords.RecordEnvironmentMeta(ctx, r.provider.Records(),
				r.plan.Class, r.plan.Slug, r.plan.Env, r.plan.Label)
		})
	}); err != nil {
		return nil, err
	}
	return r.result(promotion, flip)
}

func (r *deployRun) result(promotion edge.Promotion, flip edge.FlipBound) (*progressv1.OperationEvent, error) {
	result := &progressv1.ResultEvent{
		Success:     true,
		PromotionId: promotion.PromotionID,
		FlipBound:   flipBoundProto(&flip),
	}
	r.reportApps(result)
	for _, binding := range r.bindings {
		message, err := provider.BindingMessage(binding)
		if err != nil {
			return nil, err
		}
		result.Bindings = append(result.Bindings, message)
	}
	for _, entry := range r.plan.Apps {
		for _, fn := range r.functions[entry.App] {
			result.Functions = append(result.Functions, &progressv1.FunctionOutput{
				LogicalName: fn.Name,
				Url:         fn.URL,
			})
		}
	}
	for slot, hosts := range r.servedHostnames() {
		for _, host := range hosts {
			if r.world() == hostingProduction && !r.state.Ready(host, r.front.Kind()) {
				continue
			}
			r.outcomes[slot].Urls = append(r.outcomes[slot].Urls, "https://"+host)
		}
	}
	result.UrlNotes = r.pending
	return &progressv1.OperationEvent{Event: &progressv1.OperationEvent_Result{Result: result}}, nil
}

func (r *deployRun) publish(ctx context.Context, bindings []provider.Binding) error {
	publishing := make([]envvars.NamedBindingWrite, 0, len(bindings))
	for _, binding := range bindings {
		message, err := provider.BindingMessage(binding)
		if err != nil {
			return err
		}
		if err := envvarsserver.VerifyGrantScope(message); err != nil {
			return connect.NewError(connect.CodeInvalidArgument, err)
		}
		pair, err := envvarsserver.BindingPair(envvars.OwnerOcel, message)
		if err != nil {
			return err
		}
		publishing = append(publishing, envvars.NamedBindingWrite{Name: binding.Name, Write: pair})
	}
	if _, err := r.values.SetBindings(ctx, r.scope, bindingEnvironment(r.plan), envvars.OwnerOcel, publishing); err != nil {
		return fmt.Errorf("publish %s's bindings: %w", r.scope.Project, err)
	}
	if err := r.prune(ctx, bindings); err != nil {
		return err
	}
	r.reader().forget()
	return nil
}

func (r *deployRun) prune(ctx context.Context, bindings []provider.Binding) error {
	environment := bindingEnvironment(r.plan)
	held, err := r.values.ListBindings(ctx, r.scope, environment)
	if err != nil {
		return fmt.Errorf("read %s's published bindings: %w", r.scope.Project, err)
	}
	var stale []string
	for _, record := range held {
		if record.Owner != envvars.OwnerOcel || record.Environment != environment {
			continue
		}
		if slices.ContainsFunc(bindings, func(binding provider.Binding) bool { return binding.Name == record.Name }) {
			continue
		}
		stale = append(stale, record.Name)
	}
	if len(stale) == 0 {
		return nil
	}
	if _, err := r.values.RemoveBindings(ctx, r.scope, environment, stale); err != nil {
		return fmt.Errorf("prune %s's published bindings: %w", r.scope.Project, err)
	}
	return nil
}

type publishedBindings struct {
	store       envvars.Store
	scope       envvars.Scope
	environment string

	mu       sync.Mutex
	resolved []provider.Binding
	held     bool
}

func (p *publishedBindings) forget() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resolved, p.held = nil, false
}

func (p *publishedBindings) Published(ctx context.Context) ([]provider.Binding, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.held {
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
	bindings := make([]provider.Binding, 0, len(resolved))
	for i, published := range resolved {
		binding, err := bindingPublished(names[i], published)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	p.resolved, p.held = bindings, true
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

func bindingPublished(name string, published envvars.StoredBinding) (provider.Binding, error) {
	message, err := envvarsserver.DecodeBinding(published.Value)
	if err != nil {
		return provider.Binding{}, fmt.Errorf("read binding %s: %w", name, err)
	}
	binding := provider.BindingOf(message)
	binding.Version = published.Version
	binding.Wire = published.Value
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

func runs(images provider.ImagePushes, entry provider.AppEntry) string {
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

func frameworkOf(fn *contractv1.ManifestFunction) appbuild.Framework {
	return appbuild.Framework{Name: fn.GetFramework().GetName(), Arch: fn.GetFramework().GetArch()}
}
