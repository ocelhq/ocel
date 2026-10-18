package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/ocelhq/ocel/platform/aws/provider/resources/postgres"

	"github.com/ocelhq/ocel/platform/aws/provider/compute/container"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/function"
	awsresources "github.com/ocelhq/ocel/platform/aws/provider/resources"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/kv"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/realtime"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/functioncode"
	"github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"
	"github.com/ocelhq/ocel/platform/aws/provider/site"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/functionnetwork"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/sharedstack"
)

type Scope struct {
	Tier       environment.Tier
	Slug       string
	Env        string
	Edge       edge.Kind
	OutputRoot string
}

func scopeOf(ref provider.StackRef, kind edge.Kind) Scope {
	return Scope{Tier: ref.Tier, Slug: ref.Project, Env: ref.Name.Env, Edge: kind}
}

func edgeKindOf(spec provider.StackSpec) edge.Kind {
	if spec.Edge == nil {
		return ""
	}
	return spec.Edge.Kind()
}

type ReleaseConfig func(ctx context.Context, scope Scope) (Config, error)

type Stacks struct {
	resolve    ReleaseConfig
	realized   *Realized
	engine     pulumi.Engine
	skipChecks bool

	served *function.ServedApps

	pending    *site.PendingSets
	pluginOnce sync.Once
	plugin     pulumi.Plugin
	pluginErr  error

	mu     sync.Mutex
	opened map[Scope]*release

	containers      *containers.Stacks
	functionNetwork *functionnetwork.Stacks

	Statements postgres.StatementsAPI
}

type release struct {
	*Stacks
	cfg        Config
	registry   component.Registry
	automation *pulumi.Automation[component.Program]
}

func NewStacks(resolve ReleaseConfig, realized *Realized) *Stacks {
	return newStacks(resolve, realized, nil)
}

func newStacks(resolve ReleaseConfig, realized *Realized, engine pulumi.Engine) *Stacks {
	r := &Stacks{
		resolve:    resolve,
		realized:   realized,
		engine:     engine,
		skipChecks: processenv.SkipChecks(),
		served:     function.NewServedApps(),
		pending:    site.NewPendingSets(),
		opened:     map[Scope]*release{},
	}
	r.containers = &containers.Stacks{Open: r.openShared}
	r.functionNetwork = &functionnetwork.Stacks{Open: r.openShared}
	return r
}

func (r *Stacks) Containers() *containers.Stacks {
	return r.containers
}

func (r *Stacks) FunctionNetwork() *functionnetwork.Stacks {
	return r.functionNetwork
}

func (r *Stacks) openShared(ctx context.Context, ref provider.StackRef) (sharedstack.Owner, error) {
	owner, err := r.at(ctx, ref, "")
	if err != nil {
		return sharedstack.Owner{}, err
	}
	return sharedstack.Owner{
		Automation:     owner.automation,
		KeyValues:      owner.cfg.KeyValues,
		WrittenBy:      owner.cfg.WrittenBy,
		Region:         owner.cfg.Region,
		Subnets:        owner.cfg.Network,
		AppBoundaryARN: owner.cfg.AppBoundaryARN,
		AppRolePath:    owner.cfg.AppRolePath,
	}, nil
}

func (r *Stacks) ocelPlugin() (pulumi.Plugin, error) {
	r.pluginOnce.Do(func() { r.plugin, r.pluginErr = newOcelPlugin(r.pending, r.Statements) })
	return r.plugin, r.pluginErr
}

func (r *Stacks) at(ctx context.Context, ref provider.StackRef, kind edge.Kind) (*release, error) {
	return r.open(ctx, scopeOf(ref, kind))
}

func (r *Stacks) atSpec(ctx context.Context, spec provider.StackSpec) (*release, error) {
	scope := scopeOf(spec.Ref, edgeKindOf(spec))
	if spec.App != nil {
		scope.OutputRoot = spec.App.OutputRoot
	}
	return r.open(ctx, scope)
}

func (r *Stacks) open(ctx context.Context, scope Scope) (*release, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, opened := r.opened[scope]; opened {
		return existing, nil
	}
	cfg, err := r.resolve(ctx, scope)
	if err != nil {
		return nil, err
	}
	plugin, err := r.ocelPlugin()
	if err != nil {
		return nil, err
	}
	created := &release{Stacks: r, cfg: cfg, registry: registryOf(cfg)}
	created.automation = pulumi.New(pulumi.Config[component.Program]{
		Backend: pulumi.Backend{
			URL:        cfg.BackendURL,
			Passphrase: cfg.Passphrase,
			Project:    cfg.PulumiProject,
			Env:        map[string]string{"AWS_REGION": cfg.Region},
		},
		Program:   component.RunProgram,
		Configure: configureDefaultTags,
		Secrets:   created.secrets,
		Refresh:   r.refreshPolicy(),
		Engine:    r.engine,
		Plugins:   []pulumi.Plugin{plugin},
	})
	r.opened[scope] = created
	return created, nil
}

func registryOf(cfg Config) component.Registry {
	return awsresources.Registry(awsresources.Clients{
		Artifacts:   component.Artifacts{Objects: cfg.Objects, Bucket: cfg.ArtifactBucket},
		Clusters:    cfg.Clusters,
		KVTokens:    kv.Tokens{Parameters: cfg.Parameters, Root: cfg.KVTokenRoot},
		SigningKeys: realtime.Keys{SigningKeys: cfg.SigningKeys, Root: cfg.SigningKeyRoot},
	})
}

func (r *Stacks) refreshPolicy() func(provider.StackRef, pulumi.Operation) bool {
	return func(ref provider.StackRef, op pulumi.Operation) bool {
		if op != pulumi.OperationDestroy || r.skipChecks {
			return false
		}
		return !r.realized.realizedHere(naming.Sanitize(ref.Project), ref.Name)
	}
}

type infraWork struct {
	transformed *transformPatches
	previewing  bool
	vpc         *component.DefaultVPC
	clients     string
}

func (r *release) infraProgram(spec provider.StackSpec, work *infraWork) component.Program {
	return component.Program{
		Run: func(pctx *sdk.Context) error {
			if err := work.transformed.install(pctx); err != nil {
				return err
			}
			return r.infra(pctx, spec, work)
		},
		Tags: spec.Tags,
	}
}

func (r *release) appProgram(spec provider.StackSpec, work *appWork) component.Program {
	return component.Program{
		Run: func(pctx *sdk.Context) error {
			shipped, err := site.ShipArtifacts(pctx, r.cfg.buckets(), work.uploads)
			if err != nil {
				return err
			}
			if err := work.transformed.install(pctx); err != nil {
				return err
			}
			return work.run(pctx, shipped)
		},
		Tags:    spec.Tags,
		Secrets: r.appSecrets(spec),
	}
}

func (r *release) infra(pctx *sdk.Context, spec provider.StackSpec, work *infraWork) error {
	at := r.stackOf(spec.Ref, spec.Resources, work.transformed, work.previewing)
	if work.vpc != nil {
		subnetIDs, err := component.LookupDefaultSubnets(pctx, work.vpc.ID)
		if err != nil {
			return err
		}
		at.Network = component.Network{VPCID: work.vpc.ID, SubnetIDs: subnetIDs, Clients: work.clients}
	}
	for _, declaring := range r.registry.Components() {
		if err := declaring.Declare(pctx, at, declaring.Declared(spec.Resources)); err != nil {
			return err
		}
	}
	return nil
}

func (r *release) stackOf(ref provider.StackRef, declared []provider.Resource, transformed *transformPatches, previewing bool) component.Stack {
	return component.Stack{
		Ref:            ref,
		Project:        naming.Sanitize(ref.Project),
		Env:            ref.Name.Env,
		Region:         r.cfg.Region,
		Previewing:     previewing,
		StateTable:     r.cfg.StateTable,
		StateTableARN:  r.cfg.StateTableARN,
		AppBoundaryARN: r.cfg.AppBoundaryARN,
		AppRolePath:    r.cfg.AppRolePath,
		Transforms:     transformed,
		Resources:      declared,
	}
}

func (r *release) sweep(ctx context.Context, ref provider.StackRef, kept []provider.Resource) error {
	at := r.stackOf(ref, kept, nil, false)
	for _, swept := range r.registry.Components() {
		if swept.Sweep == nil {
			continue
		}
		if err := swept.Sweep(ctx, at, swept.Declared(kept)); err != nil {
			return err
		}
	}
	return nil
}

func configureDefaultTags(_ context.Context, program component.Program) (auto.ConfigMap, error) {
	if len(program.Tags) == 0 {
		return auto.ConfigMap{}, nil
	}
	encoded, err := json.Marshal(map[string]map[string]string{"tags": program.Tags})
	if err != nil {
		return nil, fmt.Errorf("render default tags: %w", err)
	}
	return auto.ConfigMap{"aws:defaultTags": auto.ConfigValue{Value: string(encoded)}}, nil
}

func (r *release) secrets(program component.Program) []string {
	return append([]string{r.cfg.OriginSecret, r.cfg.PreviousOriginSecret, r.cfg.PurgeSeed}, program.Secrets...)
}

func (r *release) appSecrets(spec provider.StackSpec) []string {
	secrets := slices.Collect(maps.Values(spec.App.Values.Sensitive))
	if r.cfg.PurgeSeed != "" && spec.App.ISR != nil {
		secrets = append(secrets, edge.DerivePurgeSecret(r.cfg.PurgeSeed, spec.App.ISR.Prefix))
	}
	return secrets
}

func (r *release) decodeInfra(ctx context.Context, spec provider.StackSpec, outputs auto.OutputMap) (provider.StackResult, error) {
	at := r.stackOf(spec.Ref, spec.Resources, nil, false)
	result := provider.StackResult{}
	for _, resource := range spec.Resources {
		if resource.Binding != "" {
			continue
		}
		collecting, served := r.registry.For(resource.Type)
		if !served {
			return provider.StackResult{}, r.registry.RefuseUnserved([]provider.Resource{resource})
		}
		published, err := collecting.Collect(ctx, at, resource, outputs)
		if err != nil {
			return provider.StackResult{}, err
		}
		collected := provider.PublishedBindingOf(published)
		collected.Resource = resource.Declared
		result.Bindings = append(result.Bindings, collected)
	}
	return result, nil
}

func (r *release) refuseHandover(ctx context.Context, spec provider.StackSpec) error {
	var bound []provider.Resource
	for _, resource := range spec.Resources {
		if resource.Binding != "" {
			bound = append(bound, resource)
		}
	}
	if len(bound) == 0 {
		return nil
	}
	outputs, err := r.automation.Outputs(ctx, spec.Ref, nil)
	if err != nil {
		return err
	}
	var handed []string
	for _, resource := range bound {
		if _, provisioned := outputs[resource.Name]; provisioned {
			handed = append(handed, resource.Declared)
		}
	}
	if len(handed) == 0 {
		return nil
	}
	return &HandoverError{Bindings: handed, Stack: spec.Ref.Name.String()}
}

func (r *release) packApp(spec provider.StackSpec) (variables.Bundle, error) {
	bundle, err := variables.SealApp(r.cfg.variableStore(), spec.Ref.Project, spec.App.App, spec.App.Values)
	if err != nil {
		return variables.Bundle{}, err
	}
	project, env := naming.Sanitize(spec.Ref.Project), spec.Ref.Name.Env
	topology, err := topic.QueueTopology(r.cfg.stateTable(), project, env, topic.Of(project, env, spec.App.Topics), container.WorkerServices(r.cfg.Tier, project, env, spec.App.DeploymentWorkers))
	if err != nil {
		return variables.Bundle{}, err
	}
	if bundle.Queues, err = queues.Render(topology); err != nil {
		return variables.Bundle{}, err
	}
	return bundle, nil
}

func (r *release) refuseToAdopt(ctx context.Context, spec provider.StackSpec) error {
	if spec.Recorded {
		return nil
	}
	outputs, err := r.automation.Outputs(ctx, spec.Ref, nil)
	if err != nil || len(outputs) == 0 {
		return err
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s already exists and this project has no record of it: ocel deploys over what it provisioned itself, never over what it finds. "+
			"Remove it, or deploy this project under another name",
		spec.Ref.Name)
}

func (r *Stacks) Plan(ctx context.Context, spec provider.StackSpec, progress progress.Log) (provider.Plan, error) {
	opened, err := r.atSpec(ctx, spec)
	if err != nil {
		return provider.Plan{}, err
	}
	if err := opened.refuseToAdopt(ctx, spec); err != nil {
		return provider.Plan{}, err
	}
	return opened.plan(ctx, spec, progress)
}

func (r *Stacks) PlanDestroy(ctx context.Context, ref provider.StackRef, progress progress.Log) (provider.Plan, error) {
	opened, err := r.at(ctx, ref, "")
	if err != nil {
		return provider.Plan{}, err
	}
	removal, err := opened.automation.PreviewDestroy(ctx, ref, progress)
	if err != nil {
		return provider.Plan{}, err
	}
	workerGroups, err := opened.planWorkersOfLastRelease(ctx, ref, progress)
	if err != nil {
		return provider.Plan{}, err
	}
	removal.Groups = append(removal.Groups, workerGroups...)
	for _, tornDown := range opened.registry.Components() {
		if tornDown.Teardown != nil && tornDown.Teardown.Plan != nil {
			removal = tornDown.Teardown.Plan(removal, ref)
		}
	}
	return removal, nil
}

func (r *Stacks) Provision(ctx context.Context, spec provider.StackSpec, progress progress.Log) (provider.StackResult, error) {
	opened, err := r.atSpec(ctx, spec)
	if err != nil {
		return provider.StackResult{}, err
	}
	if err := opened.refuseToAdopt(ctx, spec); err != nil {
		return provider.StackResult{}, err
	}
	return opened.provision(ctx, spec, progress)
}

func (r *release) provision(ctx context.Context, spec provider.StackSpec, progress progress.Log) (provider.StackResult, error) {
	r.realized.mark(naming.Sanitize(spec.Ref.Project), spec.Ref.Name)
	if runsContainer(spec) {
		defer func() {
			_ = resources.ReconcileImages(ctx, r.cfg.Retention, spec.Ref, spec.App.App, spec.App.Image, spec.Images.Store, progress)
		}()
		return r.provisionContainer(ctx, spec, progress)
	}
	prepared, err := r.prepare(ctx, spec, runProvision, progress)
	if err != nil {
		return provider.StackResult{}, err
	}
	defer prepared.discard()
	work := prepared.app
	if work != nil && len(work.sets) > 0 {
		r.pending.Add(work.stack, work.sets, progress)
		defer r.pending.Drop(work.stack, work.sets)
	}
	outputs, err := r.automation.Run(ctx, spec.Ref, prepared.program, spec.Lease, progress)
	if err != nil {
		return provider.StackResult{}, err
	}
	result, err := r.decode(ctx, spec, prepared, outputs)
	if err != nil {
		return provider.StackResult{}, err
	}
	if err := prepared.transformed.refuseUnclaimed(); err != nil {
		return provider.StackResult{}, err
	}
	workers, err := r.provisionWorkers(ctx, spec, work, progress)
	if err != nil {
		return provider.StackResult{}, err
	}
	result.Functions = append(result.Functions, workers...)
	if work != nil && !work.functionsAttach {
		if err := r.functionNetwork.Release(ctx, r.cfg.KeyValues, spec.Ref, progress); err != nil {
			return provider.StackResult{}, err
		}
	}
	if spec.App == nil {
		if err := r.sweep(ctx, spec.Ref, spec.Resources); err != nil {
			return provider.StackResult{}, err
		}
	}
	if work != nil {
		r.warmAndEmbed(ctx, work, result.Functions, progress)
	}
	return result, nil
}

func (r *release) warmAndEmbed(ctx context.Context, work *appWork, functions []provider.Function, progress progress.Log) {
	targets := make([]string, 0, len(functions))
	for _, fn := range functions {
		if fn.Physical != "" {
			targets = append(targets, fn.Physical)
		}
	}
	_ = r.served.Warm(ctx, targets, progress)
	refs := functioncode.Refs(work.uploads)
	for _, fn := range functions {
		if ref, staged := refs[fn.Name]; staged && fn.Physical != "" {
			_ = r.served.EmbedCode(ctx, fn.Physical, ref, progress)
		}
	}
}

func (r *release) plan(ctx context.Context, spec provider.StackSpec, progress progress.Log) (provider.Plan, error) {
	if runsContainer(spec) {
		return r.planContainer(ctx, spec, progress)
	}
	prepared, err := r.prepare(ctx, spec, runPreview, progress)
	if err != nil {
		return provider.Plan{}, err
	}
	defer prepared.discard()
	work := prepared.app
	previewed, err := r.automation.Preview(ctx, spec.Ref, prepared.program, spec.Lease, progress)
	if err != nil {
		return provider.Plan{}, err
	}
	if err := prepared.transformed.refuseUnclaimed(); err != nil {
		return provider.Plan{}, err
	}
	if work != nil && work.networkPending {
		previewed.Groups = append([]provider.ChangeGroup{functionnetwork.NewChangeGroup(spec.Ref.Tier)}, previewed.Groups...)
	}
	workerGroups, err := r.previewWorkers(ctx, spec, work, progress)
	if err != nil {
		return provider.Plan{}, err
	}
	previewed.Groups = append(previewed.Groups, workerGroups...)
	return previewed, nil
}

type preparedStack struct {
	program     component.Program
	transformed *transformPatches
	app         *appWork
}

func (p preparedStack) discard() {
	if p.app != nil {
		functioncode.Discard(p.app.uploads)
	}
}

func (r *release) decode(ctx context.Context, spec provider.StackSpec, prepared preparedStack, outputs auto.OutputMap) (provider.StackResult, error) {
	if prepared.app != nil {
		return r.decodeApp(prepared.app, outputs)
	}
	return r.decodeInfra(ctx, spec, outputs)
}

type runKind int

const (
	runProvision runKind = iota
	runPreview
)

func (r *release) prepare(ctx context.Context, spec provider.StackSpec, kind runKind, progress progress.Log) (preparedStack, error) {
	if len(spec.Images.Pushes) > 0 {
		return preparedStack{}, refusal.Refuse(refusal.CodeInvalid,
			"%s runs on serverless compute, which runs functions rather than an image, and this release pushes %d", spec.Ref.Name, len(spec.Images.Pushes))
	}
	if spec.Kind == provider.StackApp && spec.App == nil {
		return preparedStack{}, refusal.Refuse(refusal.CodeInvalid,
			"%s provisions an app and this spec names none", spec.Ref.Name)
	}
	transformed, err := transformStackSpec(ctx, r.cfg.Transform, r.registry, spec)
	if err != nil {
		return preparedStack{}, err
	}
	if spec.App == nil {
		if err := r.refuseHandover(ctx, spec); err != nil {
			return preparedStack{}, err
		}
		if err := r.registry.RefuseUnserved(spec.Resources); err != nil {
			return preparedStack{}, err
		}
		work := &infraWork{transformed: transformed, previewing: kind == runPreview}
		if err := r.placeInVPC(ctx, spec, work); err != nil {
			return preparedStack{}, err
		}
		return preparedStack{program: r.infraProgram(spec, work), transformed: transformed}, nil
	}
	var network *functionnetwork.Network
	var pending, functionsAttach bool
	if kind == runPreview {
		network, pending, err = r.readFunctionNetworkFor(ctx, spec, transformed)
	} else {
		network, functionsAttach, err = r.claimFunctionNetworkFor(ctx, spec, transformed, progress)
	}
	if err != nil {
		return preparedStack{}, err
	}
	bundle, err := r.packApp(spec)
	if err != nil {
		return preparedStack{}, fmt.Errorf("pack %s's function package: %w", spec.App.App, err)
	}
	uploads, err := functioncode.Stage(spec, bundle.Overlay())
	if err != nil {
		return preparedStack{}, err
	}
	work, err := r.appWork(spec, transformed, network, bundle, uploads)
	if err != nil {
		functioncode.Discard(uploads)
		return preparedStack{}, err
	}
	work.networkPending = pending
	work.functionsAttach = functionsAttach
	return preparedStack{program: r.appProgram(spec, work), transformed: transformed, app: work}, nil
}

func (r *Stacks) Destroy(ctx context.Context, req provider.StackDestroy, progress progress.Log) error {
	ref := req.Ref
	opened, err := r.at(ctx, ref, "")
	if err != nil {
		return err
	}
	if err := opened.destroyWorkersOfLastRelease(ctx, ref, req.Lease, progress); err != nil {
		return err
	}
	if err := opened.prepareTeardown(ctx, ref, progress); err != nil {
		return err
	}
	if err := opened.automation.Destroy(ctx, ref, req.Lease, progress); err != nil {
		return err
	}
	if ref.Name.IsInfra() {
		if err := opened.sweep(ctx, ref, nil); err != nil {
			return err
		}
	}
	if opened.cfg.Tags != nil {
		if err := opened.cfg.Tags.Sweep(ctx, naming.Sanitize(ref.Project), ref.Name); err != nil {
			return err
		}
	}
	if err := r.containers.Release(ctx, opened.cfg.KeyValues, ref, progress); err != nil {
		return err
	}
	if err := r.functionNetwork.Release(ctx, opened.cfg.KeyValues, ref, progress); err != nil {
		return err
	}
	if ref.Name.IsInfra() {
		return nil
	}
	return resources.ForgetReleases(ctx, opened.cfg.Retention, ref, ref.Name.App, req.Images, progress)
}

func (r *release) prepareTeardown(ctx context.Context, ref provider.StackRef, progress progress.Log) error {
	outputs := func(ctx context.Context) (auto.OutputMap, error) { return r.automation.Outputs(ctx, ref, nil) }
	for _, tornDown := range r.registry.Components() {
		if tornDown.Teardown == nil || tornDown.Teardown.Before == nil {
			continue
		}
		if err := tornDown.Teardown.Before(ctx, ref, outputs, progress); err != nil {
			return err
		}
	}
	return nil
}

func (r *Stacks) Outputs(ctx context.Context, ref provider.StackRef, progress progress.Log) (auto.OutputMap, error) {
	opened, err := r.at(ctx, ref, "")
	if err != nil {
		return nil, err
	}
	return opened.automation.Outputs(ctx, ref, progress)
}

var _ provider.Stacks = (*Stacks)(nil)
