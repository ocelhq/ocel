package providerkit

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	connect "connectrpc.com/connect"
	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const stackVersion = "1"

func (h *handlers) Deploy(ctx context.Context, req *contractv1.DeployRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	return streamResult(ctx, stream, func(sender *eventSender) (*progressv1.OperationEvent, error) {
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
	Promotion   Stage
	Roster      []Stage
}

func newDeployStages(plan DeployPlan) deployStages {
	s := deployStages{
		Environment: UnitStage(naming.UnitEnvironment, environmentUnitTitle,
			progressv1.Phase_PHASE_PROVISIONING, progressv1.Phase_PHASE_UPLOADING),
		Infra:     UnitStage(plan.Infra.String(), infraUnitTitle, progressv1.Phase_PHASE_PROVISIONING),
		Edge:      UnitStage(naming.UnitEdge, edgeUnitTitle, progressv1.Phase_PHASE_PROVISIONING),
		Promotion: UnitStage(naming.UnitPromotion, promotionUnitTitle, progressv1.Phase_PHASE_FINALIZING),
		Apps:      make(map[string]Stage, len(plan.Apps)),
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
	s.Roster = append(s.Roster, s.Edge, s.Promotion)
	return s
}

type deployRun struct {
	provider Provider
	gate     Gate
	features []string
	sender   *eventSender
	tracked  *stageScope
	manifest *contractv1.Manifest
	plan     DeployPlan
	stages   deployStages

	front     edge.Edge
	stack     edge.EdgeStack
	store     stackStore
	state     EdgeStackState
	wildcard  Wildcard
	previewOn string

	values    values.Store
	scope     values.Scope
	published *publishedLinks

	registry RegistryTarget
	images   ImageStore

	dry           bool
	draft         draft
	allowDegraded []string
	withheld      string

	outcomes []*progressv1.AppResult

	mu        sync.Mutex
	artifacts map[string]ArtifactRef
	membrane  ArtifactRef
	needs     NeedRecords
	links     []Link
	functions map[string][]Function
}

func (r *deployRun) recordArtifact(logical string, ref ArtifactRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.artifacts[logical] = ref
}

func (r *deployRun) artifact(logical string) (ArtifactRef, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, held := r.artifacts[logical]
	return ref, held
}

func (r *deployRun) recordFunctions(app string, functions []Function) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.functions[app] = functions
}

func (h *handlers) openDeploy(ctx context.Context, req *contractv1.DeployRequest, sender *eventSender) (*deployRun, error) {
	provider, gate, err := h.gate(req.GetEdge().GetKind())
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
	front, err := h.edgeFor(provider, req.GetEdge())
	if err != nil {
		return nil, err
	}
	features, err := RequiredFeatures(gate.Bootstrapper.Catalogue(), frameworksOf(req.GetManifest()), string(gate.Edge))
	if err != nil {
		return nil, RefusalError(err)
	}
	run := &deployRun{
		provider:  provider,
		gate:      gate,
		features:  features,
		sender:    sender,
		tracked:   newStageScope(sender),
		manifest:  req.GetManifest(),
		plan:      plan,
		front:     front,
		store:     stackStore{records: provider.Records(), name: EdgeStackRecord(plan.Class, plan.Slug)},
		values:    values.Store{Records: provider.Records(), Sealer: provider.Sealer()},
		scope:     values.Scope{Project: plan.Slug, Class: plan.Class},
		artifacts: map[string]ArtifactRef{},
		functions: map[string][]Function{},

		dry:           req.GetDry(),
		allowDegraded: req.GetEdge().GetAllowDegraded(),
	}
	if err := run.openImages(ctx, req.GetImageRegistry()); err != nil {
		return nil, err
	}
	run.published = &publishedLinks{store: run.values, scope: run.scope, environment: plan.linkEnvironment()}
	if run.state, err = run.store.read(ctx); err != nil {
		return nil, err
	}
	run.stages = newDeployStages(plan)
	run.outcomes = pendingOutcomes(plan.Apps)
	run.draft.apps = make([]Plan, len(plan.Apps))
	run.tracked.declare(run.stages.Roster...)
	sender.detailing(run.reportApps)
	return run, nil
}

func pendingOutcomes(apps []AppEntry) []*progressv1.AppResult {
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
		if err := env.phase(progressv1.Phase_PHASE_PROVISIONING, func(report Reporter) error {
			return r.settle(ctx, report)
		}); err != nil {
			return err
		}
		return env.phase(progressv1.Phase_PHASE_UPLOADING, func(report Reporter) error {
			return r.upload(ctx, report)
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
	return r.promote(ctx)
}

func (r *deployRun) settle(ctx context.Context, report Reporter) error {
	if err := r.admit(ctx, report); err != nil {
		return err
	}
	if err := r.admitDomains(ctx); err != nil {
		return err
	}
	if err := r.admitLinks(ctx, report); err != nil {
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
	return r.preflight(ctx, report)
}

func (r *deployRun) admit(ctx context.Context, report Reporter) error {
	_, err := r.gate.Admit(ctx, r.plan.Class, r.features, !r.dry, report)
	return err
}

func (r *deployRun) admitDomains(ctx context.Context) error {
	hosts := r.hostnames()
	if r.plan.Class == ClassPreview {
		wildcard, err := readWildcard(ctx, r.provider.Records())
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
		return Refuse(CodeNotReady,
			"this project declares no domains.preview wildcard and no global preview domain is in use, so a preview deploy has nowhere to serve: "+
				"declare a project-level domains.preview wildcard, or run `ocel domain use '*.preview.example.com' --preview` to serve every project's previews on one wildcard")
	}
	if len(hosts) == 0 {
		return Refuse(CodeNotReady,
			"this project declares no domains.production, so a production deploy has no hostname to serve: "+
				"declare one in your ocel config and run `ocel domain add` to provision the certificate, the edge surface and the DNS for it")
	}
	if r.state.Edge.Empty() {
		r.withheld = fmt.Sprintf(
			"this deploy is the one that creates the edge surface, so nothing is bound to %s yet: "+
				"run `ocel domain add` to settle the certificate, the surface and the DNS, then deploy again — until then there is no address of yours to print",
			strings.Join(hosts, ", "))
		return nil
	}
	for _, host := range hosts {
		if !r.state.Ready(host, r.front.Kind()) {
			return Refuse(CodeNotReady,
				"%s is not bound to the %s edge, so nothing there would answer for it: run `ocel domain add`", host, r.front.Kind())
		}
	}
	return nil
}

func (r *deployRun) rememberProject(ctx context.Context) error {
	name := ProjectRecord(r.plan.Class, r.plan.Slug)
	held, err := Held(ctx, r.provider.Records(), name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	if held.Bytes, err = json.Marshal(Project{Features: r.features}); err != nil {
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
	if r.plan.Class != ClassPreview {
		return hostingProduction
	}
	if r.wildcard.BaseDomain != "" && len(r.hostnames()) == 0 {
		return hostingGlobalPreview
	}
	return hostingProjectPreview
}

func (r *deployRun) raiseEdge(ctx context.Context) error {
	return r.tracked.unit(r.stages.Edge, func(u *unitRun) error {
		return u.phase(progressv1.Phase_PHASE_PROVISIONING, func(report Reporter) error {
			if r.dry {
				report.Say(fmt.Sprintf("Reading the %s edge", r.front.Kind()))
				r.draft.edge = r.drawEdge()
				return nil
			}
			report.Say(fmt.Sprintf("Reconciling the %s edge", r.front.Kind()))
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
	program, err := edgeProgramFor(ctx, r.provider, r.front, EdgeProgramRequest{
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

func (r *deployRun) previewBase() (string, error) {
	var base string
	for _, host := range r.hostnames() {
		resolved, wildcard := strings.CutPrefix(host, "*.")
		if !wildcard {
			return "", Refuse(CodeInvalid,
				"this project declares the preview domain %q, which is not a `*.` wildcard: every preview is served on its own subdomain of it, so declare %q instead",
				host, edge.PreviewWildcard(host))
		}
		if base != "" && resolved != base {
			return "", Refuse(CodeInvalid,
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

func (r *deployRun) hostnames() []string {
	tier := environmentTier(r.plan.Class)
	for _, domains := range r.manifest.GetDomains() {
		if domains.GetTier() == tier {
			return domains.GetHostnames()
		}
	}
	return nil
}

func (r *deployRun) previewSite() edge.PreviewSite {
	if r.world() == hostingGlobalPreview {
		return edge.SharedPreview(r.plan.Slug, r.previewOn)
	}
	return edge.ProjectPreview(r.previewOn)
}

func (r *deployRun) servedHostnames() []string {
	if r.plan.Class != ClassPreview {
		return r.hostnames()
	}
	return r.previewSite().Hosts(r.plan.Pointer, r.appNames())
}

func (r *deployRun) checkpoint(ctx context.Context) error {
	r.state.Edge = r.stack.State()
	if r.plan.Class == ClassPreview {
		r.state.Edge.GlobalPreview = r.globalPreview()
	}
	return r.store.write(ctx, r.state)
}

func (r *deployRun) checkNeeds(ctx context.Context) error {
	check := NeedCheck{
		Edge:          r.front,
		Root:          ArtifactRoot(),
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

func (r *deployRun) preflight(ctx context.Context, report Reporter) error {
	preflighter, ok := r.provider.(DeployPreflighter)
	if !ok {
		return nil
	}
	resources, err := manifestResources(r.manifest)
	if err != nil {
		return err
	}
	grants, err := r.reader().Published(ctx)
	if err != nil {
		return err
	}
	apps, err := r.usage(resources, grants)
	if err != nil {
		return err
	}
	return preflighter.PreflightDeploy(ctx, DeployPreflight{
		Plan:      r.plan,
		Edge:      r.front.Kind(),
		Resources: resources,
		Grants:    grants,
		Apps:      apps,
		Report:    report,
	})
}

func (r *deployRun) usage(resources []Resource, published []Link) ([]AppUsage, error) {
	apps := make([]AppUsage, 0, len(r.plan.Apps))
	for _, entry := range r.plan.Apps {
		used, err := r.used(entry.App)
		if err != nil {
			return nil, err
		}
		usage := AppUsage{App: entry.App}
		for _, resource := range resources {
			if used[boundName(resource)] {
				usage.Resources = append(usage.Resources, resource)
			}
		}
		for _, link := range published {
			if used[link.Name] {
				usage.Grants = append(usage.Grants, link)
			}
		}
		apps = append(apps, usage)
	}
	return apps, nil
}

func boundName(resource Resource) string {
	if resource.Linked {
		return resource.Declared
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
		return u.phase(progressv1.Phase_PHASE_PROVISIONING, func(report Reporter) error {
			if err := r.refuseToAdopt(ctx, r.plan.Infra); err != nil {
				return err
			}
			stack := StackPlan{
				Ref:       r.ref(r.plan.Infra),
				Kind:      StackInfra,
				Edge:      r.front,
				Tags:      r.plan.infraTags(),
				Resources: resources,
				Links:     r.reader(),
			}
			if r.dry {
				report.Say("Planning the environment's infrastructure")
				drawn, err := r.provider.Releases().Plan(ctx, stack, report)
				if err != nil {
					return err
				}
				r.draft.infra = drawn
				r.draft.parameters, err = r.drawValues(ctx)
				return err
			}
			report.Say("Provisioning the environment's infrastructure")
			result, err := r.provider.Releases().Provision(ctx, stack, report)
			if err != nil {
				return err
			}
			for _, link := range result.Links {
				if err := VerifyProperties(link); err != nil {
					return err
				}
			}
			if err := r.publish(ctx, result.Links); err != nil {
				return err
			}
			r.links = result.Links
			return WriteStack(ctx, r.provider.Records(), r.plan.Class, r.plan.Slug, r.plan.Infra, Stack{
				Kind:   StackInfra,
				Links:  result.Links,
				Writer: WriterFor(""),
			})
		})
	})
}

func (r *deployRun) provisionApp(ctx context.Context, slot int, entry AppEntry) error {
	return r.tracked.unit(r.stages.Apps[entry.App], func(u *unitRun) error {
		return u.phase(progressv1.Phase_PHASE_PROVISIONING, func(report Reporter) error {
			if err := r.refuseToAdopt(ctx, entry.Stack); err != nil {
				return err
			}
			if r.dry {
				report.Say("Planning " + entry.App)
			} else {
				report.Say("Provisioning " + entry.App)
			}
			grants, err := r.grants(ctx, entry)
			if err != nil {
				return err
			}
			facts, err := r.serving(entry)
			if err != nil {
				return err
			}
			values, err := r.appValues(entry, grants)
			if err != nil {
				return err
			}
			pack, err := r.pack(ctx, entry, values, report)
			if err != nil {
				return err
			}
			staged, err := r.stageApp(entry, pack, facts.Routing)
			if err != nil {
				return err
			}
			defer discardStaged(staged)
			images, err := r.imagePlan(entry)
			if err != nil {
				return err
			}
			plan := StackPlan{
				Ref:     r.ref(entry.Stack),
				Kind:    StackApp,
				Edge:    r.front,
				Tags:    r.plan.tags(entry),
				Uploads: staged,
				Images:  images,
				Links:   r.reader(),
				App: &AppPlan{
					App:             entry.App,
					Framework:       entry.Manifest.GetFramework(),
					Entry:           entryFunction(r.manifest, entry.App),
					Deployment:      entry.Build.DeploymentID(),
					Compute:         entry.Compute(),
					Functions:       r.functionSpecs(entry),
					Image:           entry.Image,
					HealthCheckPath: entry.HealthCheckPath,
					Values:          values,
					Grants:          grants,
					Routing:         facts.Routing,
					ISR:             facts.ISR,
					Bytecode:        facts.Bytecode,
					AssetPrefix:     facts.AssetPrefix,
					Membrane:        r.membrane,
					Guard:           facts.Guard,
					Packed:          pack.Carry,
					CrossesMembrane: crossesMembrane(r.crossesMembrane, grants),
				},
			}
			if r.dry {
				drawn, err := r.provider.Releases().Plan(ctx, plan, report)
				if err != nil {
					return err
				}
				r.draft.apps[slot] = drawn
				return nil
			}
			result, err := r.provider.Releases().Provision(ctx, plan, report)
			if err != nil {
				return err
			}
			r.recordFunctions(entry.App, result.Functions)
			if err := r.warm(ctx, result.Functions, report); err != nil {
				return err
			}
			if err := r.embed(ctx, entry, result.Functions, report); err != nil {
				return err
			}
			if err := r.stage(ctx, entry, result.Functions, grants); err != nil {
				return err
			}
			return WriteStack(ctx, r.provider.Records(), r.plan.Class, r.plan.Slug, entry.Stack, Stack{
				Kind:       StackApp,
				App:        entry.App,
				Release:    entry.Build.Release().String(),
				Identity:   entry.Build.String(),
				Functions:  result.Functions,
				Containers: result.Containers,
				Writer:     WriterFor(""),
			})
		})
	})
}

func (r *deployRun) refuseToAdopt(ctx context.Context, stack naming.StackName) error {
	inspector, inspects := r.provider.(StackInspector)
	if !inspects {
		return nil
	}
	_, recorded, err := ReadStack(ctx, r.provider.Records(), r.plan.Class, r.plan.Slug, stack)
	if err != nil || recorded {
		return err
	}
	state, err := inspector.Inspect(ctx, r.ref(stack))
	if err != nil {
		return err
	}
	if !state.Present {
		return nil
	}
	return Refuse(CodeNotReady,
		"%s is already standing and this project has no record of it: ocel deploys over what it stood up itself, never over what it finds. "+
			"Remove it, or deploy this project under another name",
		stack)
}

func (r *deployRun) serving(entry AppEntry) (ServingFacts, error) {
	return ServingFactsFor(ServingQuery{
		Root:              ArtifactRoot(),
		Project:           naming.Sanitize(r.plan.Slug),
		App:               entry.App,
		Framework:         entry.Manifest.GetFramework(),
		Stack:             entry.Stack,
		Coordinate:        r.plan.coordinate(entry.App, entry.Build.Release()),
		EdgeRunsCode:      r.front.Facts().RunsCode,
		EdgeSignsForwards: r.front.Facts().SignsOriginForwards,
	})
}

func (r *deployRun) ref(stack naming.StackName) StackRef {
	return StackRef{Project: r.plan.Slug, Class: r.plan.Class, Name: stack}
}

func (r *deployRun) reader() *publishedLinks { return r.published }

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

func (r *deployRun) grants(ctx context.Context, entry AppEntry) ([]Link, error) {
	used, err := r.used(entry.App)
	if err != nil {
		return nil, err
	}
	var grants []Link
	for _, link := range r.links {
		if used[link.Name] {
			grants = append(grants, link)
		}
	}
	consumed, err := r.reader().Published(ctx)
	if err != nil {
		return nil, err
	}
	for _, link := range consumed {
		if !used[link.Name] {
			continue
		}
		if !slices.ContainsFunc(grants, func(held Link) bool { return held.Name == link.Name }) {
			grants = append(grants, link)
		}
	}
	slices.SortFunc(grants, func(a, b Link) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	if verifier, ok := r.provider.(GrantVerifier); ok {
		for _, link := range grants {
			if err := verifier.VerifyGrants(ctx, link); err != nil {
				return nil, err
			}
		}
	}
	return grants, nil
}

func (r *deployRun) appValues(entry AppEntry, grants []Link) (AppValues, error) {
	held := AppValues{
		Plain:     map[string]string{},
		Sensitive: map[string]string{},
		Owners:    map[string]string{},
		Links:     grants,
		Folder:    entry.Manifest.GetFolder(),
	}
	for _, variable := range entry.Manifest.GetVariables() {
		switch variable.GetClass() {
		case resourcesv1.VariableClass_VARIABLE_CLASS_SECRET:
			held.Secrets = append(held.Secrets, SecretRef{Key: variable.GetKey(), Folder: variable.GetFolder()})
		case resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE:
			held.Sensitive[variable.GetKey()] = variable.GetValue()
		case resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN:
			held.Plain[variable.GetKey()] = variable.GetValue()
		default:
			return AppValues{}, Refuse(CodeInvalid,
				"%s declares %s with class %s, which this deploy cannot deliver to a function; declare it as `plain`, `sensitive` or `secret`",
				entry.App, variable.GetKey(), variable.GetClass())
		}
		held.Owners[variable.GetKey()] = entry.App
	}
	return held, nil
}

func (r *deployRun) functionSpecs(entry AppEntry) []FunctionSpec {
	var specs []FunctionSpec
	for _, fn := range r.manifest.GetFunctions() {
		if fn.GetApp() != entry.App {
			continue
		}
		artifact, _ := r.artifact(fn.GetLogicalName())
		specs = append(specs, FunctionSpec{
			Name:     fn.GetLogicalName(),
			Route:    fn.GetRouteId(),
			Handler:  fn.GetHandler(),
			Runtime:  fn.GetRuntime(),
			Artifact: artifact,
			URL:      true,
		})
	}
	return specs
}

func frameworksOf(manifest *contractv1.Manifest) []string {
	var frameworks []string
	for _, app := range manifest.GetApps() {
		if name := app.GetFramework(); name != "" && !slices.Contains(frameworks, name) {
			frameworks = append(frameworks, name)
		}
	}
	slices.Sort(frameworks)
	return frameworks
}

func entryFunction(manifest *contractv1.Manifest, app string) string {
	for _, fn := range manifest.GetFunctions() {
		if fn.GetApp() == app && fn.GetRouteId() == "" {
			return fn.GetLogicalName()
		}
	}
	return ""
}

func (r *deployRun) embed(ctx context.Context, entry AppEntry, functions []Function, report Reporter) error {
	embedder, embeds := r.provider.(CodeEmbedder)
	if !embeds {
		return nil
	}
	for _, fn := range functions {
		ref, held := r.artifact(fn.Name)
		if !held {
			continue
		}
		if err := embedder.EmbedCode(ctx, fn.Physical, ref, report); err != nil {
			return fmt.Errorf("embed %s's bytecode cache for %s: %w", fn.Name, entry.App, err)
		}
	}
	return nil
}

func (r *deployRun) warm(ctx context.Context, functions []Function, report Reporter) error {
	warmer, warms := r.provider.(Warmer)
	if !warms || len(functions) == 0 {
		return nil
	}
	targets := make([]string, 0, len(functions))
	for _, fn := range functions {
		if fn.Physical != "" {
			targets = append(targets, fn.Physical)
		}
	}
	return warmer.Warm(ctx, targets, report)
}

func (r *deployRun) stage(ctx context.Context, entry AppEntry, functions []Function, grants []Link) error {
	urls := make(map[string]string, len(functions))
	for _, fn := range functions {
		urls[fn.Name] = fn.URL
	}
	coordinate := r.plan.coordinate(entry.App, entry.Build.Release())
	record := edge.DeploymentRecord{
		App:              entry.App,
		Framework:        entry.Manifest.GetFramework(),
		Identity:         entry.Build.String(),
		DeploymentID:     entry.Build.DeploymentID(),
		Entry:            entryFunction(r.manifest, entry.App),
		FunctionURLs:     urls,
		AssetPrefix:      coordinate.AssetKey(""),
		IsrPrefix:        coordinate.ISRPrefix(),
		CreatedAt:        time.Now().Unix(),
		ValueFingerprint: entry.Build.Fingerprint(),
		Needs:            r.needs[entry.App].Needs,
		SupportInEffect:  r.needs[entry.App].InEffect,
		Waived:           r.needs[entry.App].Waived,
	}
	if len(grants) > 0 {
		record.Env = map[string]string{}
		for _, link := range grants {
			record.Env[link.Name] = string(link.Type)
		}
	}
	return r.stack.Ledger().PutStaged(ctx, record)
}

func (r *deployRun) promote(ctx context.Context) (*progressv1.OperationEvent, error) {
	if r.dry {
		r.draft.promotion = r.drawPromotion()
		r.sender.send(planEvent(r.drawn()))
		return okResult(), nil
	}
	flip := r.front.FlipBound()
	promotion := edge.Promotion{
		PromotionID: r.plan.PromotionID,
		Ts:          time.Now().Unix(),
		Builds:      r.plan.Builds,
		Tag:         r.plan.Tag,
		Flip:        &flip,
	}
	if err := r.tracked.unit(r.stages.Promotion, func(u *unitRun) error {
		return u.phase(progressv1.Phase_PHASE_FINALIZING, func(report Reporter) error {
			report.Say("Promoting the deployment")
			if err := r.stack.Promote(ctx, promotion, r.plan.Pointer, report); err != nil {
				return err
			}
			if err := r.checkpoint(ctx); err != nil {
				return err
			}
			if r.plan.Class != ClassPreview {
				return nil
			}
			return recordEnvironmentMeta(ctx, r.provider.Records(),
				r.plan.Class, r.plan.Slug, r.plan.Env, r.plan.Label, r.plan.Ephemeral)
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
	for _, link := range r.links {
		message, err := LinkMessage(link)
		if err != nil {
			return nil, err
		}
		result.Links = append(result.Links, message)
	}
	for _, entry := range r.plan.Apps {
		for _, fn := range r.functions[entry.App] {
			result.Functions = append(result.Functions, &progressv1.FunctionOutput{
				LogicalName: fn.Name,
				Url:         fn.URL,
			})
		}
	}
	for _, host := range r.servedHostnames() {
		result.AppUrls = append(result.AppUrls, "https://"+host)
	}
	if r.withheld != "" {
		result.AppUrls, result.UrlNote = nil, r.withheld
	}
	return &progressv1.OperationEvent{Event: &progressv1.OperationEvent_Result{Result: result}}, nil
}

func (r *deployRun) publish(ctx context.Context, links []Link) error {
	publishing := make([]values.Publishing, 0, len(links))
	for _, link := range links {
		message, err := LinkMessage(link)
		if err != nil {
			return err
		}
		if err := VerifyGrantScope(message); err != nil {
			return linksError(err)
		}
		pair, err := LinkPair(values.OwnerOcel, message)
		if err != nil {
			return err
		}
		publishing = append(publishing, values.Publishing{Name: link.Name, Pair: pair})
	}
	if _, err := r.values.SetLinks(ctx, r.scope, r.plan.linkEnvironment(), values.OwnerOcel, publishing); err != nil {
		return fmt.Errorf("publish %s's links: %w", r.scope.Project, err)
	}
	if err := r.prune(ctx, links); err != nil {
		return err
	}
	r.reader().forget()
	return nil
}

func (r *deployRun) prune(ctx context.Context, links []Link) error {
	environment := r.plan.linkEnvironment()
	held, err := r.values.ListLinks(ctx, r.scope, environment)
	if err != nil {
		return fmt.Errorf("read %s's published links: %w", r.scope.Project, err)
	}
	var stale []string
	for _, record := range held {
		if record.Owner != values.OwnerOcel || record.Environment != environment {
			continue
		}
		if slices.ContainsFunc(links, func(link Link) bool { return link.Name == record.Name }) {
			continue
		}
		stale = append(stale, record.Name)
	}
	if len(stale) == 0 {
		return nil
	}
	if _, err := r.values.RemoveLinks(ctx, r.scope, environment, stale); err != nil {
		return fmt.Errorf("prune %s's published links: %w", r.scope.Project, err)
	}
	return nil
}

type publishedLinks struct {
	store       values.Store
	scope       values.Scope
	environment string

	mu       sync.Mutex
	resolved []Link
	held     bool
}

func (p *publishedLinks) forget() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resolved, p.held = nil, false
}

func (p *publishedLinks) Published(ctx context.Context) ([]Link, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.held {
		return p.resolved, nil
	}
	names, err := p.store.PublishedNames(ctx, p.scope, p.environment)
	if err != nil {
		return nil, err
	}
	resolved, err := p.store.ResolveLinks(ctx, p.scope, p.environment, names)
	if err != nil {
		return nil, err
	}
	links := make([]Link, 0, len(resolved))
	for i, published := range resolved {
		link, err := linkPublished(names[i], published)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	p.resolved, p.held = links, true
	return links, nil
}

func (p *publishedLinks) Names(ctx context.Context) ([]string, error) {
	links, err := p.Published(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(links))
	for _, link := range links {
		names = append(names, link.Name)
	}
	return names, nil
}

func (p *publishedLinks) Resolve(ctx context.Context, name string) (Link, error) {
	links, err := p.Published(ctx)
	if err != nil {
		return Link{}, err
	}
	for _, link := range links {
		if link.Name == name {
			return link, nil
		}
	}
	published, err := p.store.ResolveLink(ctx, p.scope, p.environment, name)
	if err != nil {
		return Link{}, err
	}
	return linkPublished(name, published)
}

func linkPublished(name string, published values.Published) (Link, error) {
	message, err := DecodeLink(published.Value)
	if err != nil {
		return Link{}, fmt.Errorf("read link %s: %w", name, err)
	}
	link := linkOf(message)
	link.Version = published.Version
	return link, nil
}

func linkOf(message *linksv1.Link) Link {
	link := Link{
		Type:       LinkCustom,
		Name:       message.GetName(),
		Source:     message.GetSource(),
		Properties: map[string]string{},
		Grants:     GrantsOf(message),
	}
	if kind, known := linkTypes[naming.LinkTypeOf(message)]; known {
		link.Type = kind
	}
	for _, name := range naming.LinkPropertyNames(message) {
		if value, held := naming.LinkProperty(message, name); held {
			link.Properties[name] = fmt.Sprint(value)
		}
	}
	return link
}

func (r *deployRun) openImages(ctx context.Context, wired *contractv1.ImageRegistry) error {
	r.registry = RegistryTarget{
		Server:    wired.GetServer(),
		Namespace: wired.GetNamespace(),
		Username:  wired.GetUsername(),
		Password:  wired.GetPassword(),
	}
	store, err := imageStoreFor(ctx, r.provider, r.registry)
	if err != nil {
		return err
	}
	r.images = store
	return nil
}

func (r *deployRun) imagePlan(entry AppEntry) (ImagePlan, error) {
	if r.images == nil || entry.Compute() != ComputeContainer || entry.Image == "" {
		return ImagePlan{}, nil
	}
	push, err := imagePush(entry.App, entry.Image, r.registry)
	if err != nil {
		return ImagePlan{}, err
	}
	return ImagePlan{Store: r.images, Pushes: []ImagePush{push}}, nil
}
