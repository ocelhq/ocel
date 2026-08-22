package providerkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const stackVersion = "1"

func (h *handlers) Deploy(ctx context.Context, req *contractv1.DeployRequest, stream *connect.ServerStream[progressv1.OperationEvent]) (err error) {
	sender := newEventSender(ctx, stream.Send)
	defer func() { err = errors.Join(err, sender.close()) }()

	run, err := h.openDeploy(ctx, req, sender)
	if err != nil {
		return sender.fail(refusalError(err))
	}
	result, err := run.execute(ctx)
	if err != nil {
		return sender.fail(refusalError(err))
	}
	sender.send(result)
	return nil
}

type deployStages struct {
	Uploading    Stage
	Provisioning Stage
	Infra        Stage
	Apps         map[string]Stage
	Promoting    Stage
}

func (s deployStages) declared(plan DeployPlan) []Stage {
	stages := []Stage{s.Uploading, s.Provisioning}
	if !plan.Infra.IsZero() {
		stages = append(stages, s.Infra)
	}
	for _, entry := range plan.Apps {
		stages = append(stages, s.Apps[entry.App])
	}
	return append(stages, s.Promoting)
}

type deployRun struct {
	provider Provider
	sender   *eventSender
	tracer   *eventTracer
	manifest *contractv1.Manifest
	plan     DeployPlan
	stages   deployStages

	front edge.Edge
	stack edge.EdgeStack
	store stackStore
	state EdgeStackState

	values values.Store
	scope  values.Scope

	artifacts map[string]ArtifactRef
	needs     NeedRecords
	links     []Link
	functions map[string][]Function
}

func (h *handlers) openDeploy(ctx context.Context, req *contractv1.DeployRequest, sender *eventSender) (*deployRun, error) {
	provider, err := h.session.use()
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
	run := &deployRun{
		provider:  provider,
		sender:    sender,
		tracer:    newEventTracer(sender),
		manifest:  req.GetManifest(),
		plan:      plan,
		front:     front,
		store:     stackStore{records: provider.Records(), name: EdgeStackRecord(plan.Class, plan.Slug)},
		values:    values.Store{Records: provider.Records(), Sealer: provider.Sealer()},
		scope:     values.Scope{Project: plan.Slug, Class: plan.Class},
		artifacts: map[string]ArtifactRef{},
		functions: map[string][]Function{},
	}
	if run.state, err = run.store.read(ctx); err != nil {
		return nil, err
	}
	run.stages = deployStages{
		Uploading:    NewRootStage("Uploading"),
		Provisioning: NewRootStage("Provisioning"),
		Promoting:    NewRootStage("Promoting"),
		Apps:         map[string]Stage{},
	}
	run.stages.Infra = NewStage(run.stages.Provisioning, plan.Infra.String())
	for _, entry := range plan.Apps {
		run.stages.Apps[entry.App] = NewStage(run.stages.Provisioning, entry.App)
	}
	return run, nil
}

func (r *deployRun) execute(ctx context.Context) (*progressv1.OperationEvent, error) {
	r.tracer.DeclareStages(true, r.stages.declared(r.plan)...)

	if err := r.rememberProject(ctx); err != nil {
		return nil, err
	}
	if err := r.reconcileEdge(ctx); err != nil {
		return nil, err
	}
	if err := r.checkNeeds(ctx); err != nil {
		return nil, err
	}
	if err := r.timed(r.stages.Uploading, func(report Reporter) error { return r.upload(ctx, report) }); err != nil {
		return nil, err
	}
	if err := r.provision(ctx); err != nil {
		return nil, err
	}
	return r.promote(ctx)
}

func (r *deployRun) report(stage Stage, phase progressv1.Phase) Reporter {
	return &reporter{sender: r.sender, tracer: r.tracer, stage: stage, phase: phase}
}

func (r *deployRun) timed(stage Stage, do func(Reporter) error) error {
	start := time.Now()
	err := do(r.report(stage, phaseOf(stage, r.stages)))
	r.tracer.Span(stage.ID, stage.ParentID, stage.Title, start, time.Now(), err)
	return err
}

func phaseOf(stage Stage, stages deployStages) progressv1.Phase {
	switch stage.ID {
	case stages.Uploading.ID:
		return progressv1.Phase_PHASE_UPLOADING
	case stages.Promoting.ID:
		return progressv1.Phase_PHASE_FINALIZING
	default:
		return progressv1.Phase_PHASE_PROVISIONING
	}
}

func (r *deployRun) rememberProject(ctx context.Context) error {
	name := ProjectRecord(r.plan.Slug)
	held, err := Held(ctx, r.provider.Records(), name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	if len(held.Bytes) > 0 {
		return nil
	}
	if held.Bytes, err = json.Marshal(Project{}); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	if _, err := r.provider.Records().Write(ctx, held); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return nil
}

func (r *deployRun) reconcileEdge(ctx context.Context) error {
	spec := edge.StackSpec{
		Version: stackVersion,
		Class:   r.plan.Class,
		Slug:    r.plan.Slug,
		Domains: r.hostnames(),
	}
	stack, err := r.front.Reconcile(ctx, spec, r.state.Edge)
	if err != nil {
		return err
	}
	r.stack = stack
	return r.checkpoint(ctx)
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

func (r *deployRun) checkpoint(ctx context.Context) error {
	r.state.Edge = r.stack.State()
	return r.store.write(ctx, r.state)
}

func (r *deployRun) checkNeeds(ctx context.Context) error {
	check := NeedCheck{
		Edge: r.front,
		Root: ArtifactRoot(),
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

func (r *deployRun) provision(ctx context.Context) error {
	if err := r.provisionInfra(ctx); err != nil {
		return err
	}
	for _, entry := range r.plan.Apps {
		if err := r.provisionApp(ctx, entry); err != nil {
			return err
		}
	}
	return nil
}

func (r *deployRun) provisionInfra(ctx context.Context) error {
	if r.plan.Infra.IsZero() {
		return nil
	}
	resources, err := manifestResources(r.manifest)
	if err != nil {
		return err
	}
	return r.timed(r.stages.Infra, func(report Reporter) error {
		if err := r.refuseToAdopt(ctx, r.plan.Infra); err != nil {
			return err
		}
		report.Say("Provisioning the environment's infrastructure")
		result, err := r.provider.Releases().Provision(ctx, StackPlan{
			Ref:       r.ref(r.plan.Infra),
			Kind:      StackInfra,
			Tags:      r.plan.infraTags(),
			Resources: resources,
			Links:     r.reader(),
		}, report)
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
		return WriteStack(ctx, r.provider.Records(), r.plan.Slug, r.plan.Infra, Stack{
			Kind:   StackInfra,
			Links:  result.Links,
			Writer: WriterFor(""),
		})
	})
}

func (r *deployRun) provisionApp(ctx context.Context, entry AppEntry) error {
	return r.timed(r.stages.Apps[entry.App], func(report Reporter) error {
		if err := r.refuseToAdopt(ctx, entry.Stack); err != nil {
			return err
		}
		report.Say("Provisioning " + entry.App)
		grants, err := r.grants(ctx, entry)
		if err != nil {
			return err
		}
		plan := StackPlan{
			Ref:   r.ref(entry.Stack),
			Kind:  StackApp,
			Tags:  r.plan.tags(entry),
			Links: r.reader(),
			App: &AppPlan{
				App:       entry.App,
				Framework: entry.Manifest.GetFramework(),
				Entry:     entryFunction(r.manifest, entry.App),
				Functions: r.functionSpecs(entry),
				Values:    r.appValues(entry, grants),
				Grants:    grants,
			},
		}
		result, err := r.provider.Releases().Provision(ctx, plan, report)
		if err != nil {
			return err
		}
		r.functions[entry.App] = result.Functions
		if err := r.embed(ctx, entry, result.Functions, report); err != nil {
			return err
		}
		if err := r.warm(ctx, result.Functions, report); err != nil {
			return err
		}
		if err := r.stage(ctx, entry, result.Functions, grants); err != nil {
			return err
		}
		return WriteStack(ctx, r.provider.Records(), r.plan.Slug, entry.Stack, Stack{
			Kind:      StackApp,
			App:       entry.App,
			Release:   entry.Build.Release().String(),
			Identity:  entry.Build.String(),
			Functions: result.Functions,
			Writer:    WriterFor(""),
		})
	})
}

func (r *deployRun) refuseToAdopt(ctx context.Context, stack naming.StackName) error {
	inspector, inspects := r.provider.(StackInspector)
	if !inspects {
		return nil
	}
	_, recorded, err := ReadStack(ctx, r.provider.Records(), r.plan.Slug, stack)
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

func (r *deployRun) ref(stack naming.StackName) StackRef {
	return StackRef{Project: r.plan.Slug, Class: r.plan.Class, Name: stack}
}

func (r *deployRun) reader() LinkReader {
	return publishedLinks{store: r.values, scope: r.scope, environment: r.plan.Env}
}

func (r *deployRun) grants(ctx context.Context, entry AppEntry) ([]Link, error) {
	grants := slices.Clone(r.links)
	consumed, err := r.reader().Published(ctx)
	if err != nil {
		return nil, err
	}
	for _, link := range consumed {
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

func (r *deployRun) appValues(entry AppEntry, grants []Link) AppValues {
	held := AppValues{
		Plain:     map[string]string{},
		Sensitive: map[string]string{},
		Secrets:   map[string]string{},
		Owners:    map[string]string{},
		Links:     grants,
	}
	for _, variable := range entry.Manifest.GetVariables() {
		switch variable.GetClass() {
		case resourcesv1.VariableClass_VARIABLE_CLASS_SECRET:
			held.Secrets[variable.GetKey()] = variable.GetValue()
		case resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE:
			held.Sensitive[variable.GetKey()] = variable.GetValue()
		default:
			held.Plain[variable.GetKey()] = variable.GetValue()
		}
		held.Owners[variable.GetKey()] = entry.App
	}
	return held
}

func (r *deployRun) functionSpecs(entry AppEntry) []FunctionSpec {
	var specs []FunctionSpec
	for _, fn := range r.manifest.GetFunctions() {
		if fn.GetApp() != "" && fn.GetApp() != entry.App {
			continue
		}
		specs = append(specs, FunctionSpec{
			Name:     fn.GetLogicalName(),
			Handler:  fn.GetHandler(),
			Runtime:  fn.GetRuntime(),
			Artifact: r.artifacts[fn.GetLogicalName()],
			URL:      true,
		})
	}
	return specs
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
		ref, held := r.artifacts[fn.Name]
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
	flip := r.front.FlipBound()
	promotion := edge.Promotion{
		PromotionID: r.plan.PromotionID,
		Ts:          time.Now().Unix(),
		Builds:      r.plan.Builds,
		Tag:         r.plan.Tag,
		Flip:        &flip,
	}
	if err := r.timed(r.stages.Promoting, func(report Reporter) error {
		report.Say("Promoting the deployment")
		if err := r.stack.Promote(ctx, promotion, r.plan.Pointer); err != nil {
			return err
		}
		return r.checkpoint(ctx)
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
	if front := r.stack.State().Front; front != "" {
		result.AppUrls = []string{"https://" + front}
	}
	return &progressv1.OperationEvent{Event: &progressv1.OperationEvent_Result{Result: result}}, nil
}

func (r *deployRun) publish(ctx context.Context, links []Link) error {
	for _, link := range links {
		message, err := LinkMessage(link)
		if err != nil {
			return err
		}
		pair, err := linkPair(values.OwnerOcel, message)
		if err != nil {
			return err
		}
		if _, err := r.values.SetLink(ctx, r.scope, r.plan.Env, values.OwnerOcel, link.Name, pair); err != nil {
			return fmt.Errorf("publish link %s: %w", link.Name, err)
		}
	}
	return nil
}

type publishedLinks struct {
	store       values.Store
	scope       values.Scope
	environment string
}

func (p publishedLinks) Published(ctx context.Context) ([]Link, error) {
	names, err := p.store.PublishedNames(ctx, p.scope, p.environment)
	if err != nil {
		return nil, err
	}
	links := make([]Link, 0, len(names))
	for _, name := range names {
		link, err := p.resolve(ctx, name)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, nil
}

func (p publishedLinks) Resolve(ctx context.Context, name, property string) (string, error) {
	link, err := p.resolve(ctx, name)
	if err != nil {
		return "", err
	}
	value, held := link.Properties[property]
	if !held {
		return "", Refuse(CodeInvalid, "link %s carries no property %q", name, property)
	}
	return value, nil
}

func (p publishedLinks) resolve(ctx context.Context, name string) (Link, error) {
	published, err := p.store.ResolveLink(ctx, p.scope, p.environment, name)
	if err != nil {
		return Link{}, err
	}
	message, err := DecodeLink(published.Value)
	if err != nil {
		return Link{}, fmt.Errorf("read link %s: %w", name, err)
	}
	return linkOf(message), nil
}

func linkOf(message *linksv1.Link) Link {
	link := Link{
		Type:       LinkCustom,
		Name:       message.GetName(),
		Properties: map[string]string{},
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
