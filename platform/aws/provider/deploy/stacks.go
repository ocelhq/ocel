package deploy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	ec2 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	scheduler "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/scheduler"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

type Scope struct {
	Tier environment.Tier
	Slug string
	Env  string
	Edge edge.Kind
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
	resolve  ReleaseConfig
	realized *Realized
	engine   pulumi.Engine

	served *servedApps

	pending    *pendingSets
	pluginOnce sync.Once
	plugin     pulumi.Plugin
	pluginErr  error

	mu     sync.Mutex
	opened map[Scope]*release

	containerInfraLock sync.Mutex
}

type release struct {
	*Stacks
	cfg        Config
	automation *pulumi.Automation
}

func NewStacks(resolve ReleaseConfig, realized *Realized) *Stacks {
	return newStacks(resolve, realized, nil)
}

func newStacks(resolve ReleaseConfig, realized *Realized, engine pulumi.Engine) *Stacks {
	return &Stacks{
		resolve:  resolve,
		realized: realized,
		engine:   engine,
		served:   newServedApps(),
		pending:  newPendingSets(),
		opened:   map[Scope]*release{},
	}
}

func (r *Stacks) assetSetPlugin() (pulumi.Plugin, error) {
	r.pluginOnce.Do(func() { r.plugin, r.pluginErr = assetSetPlugin(r.pending) })
	return r.plugin, r.pluginErr
}

func (r *Stacks) at(ctx context.Context, ref provider.StackRef, kind edge.Kind) (*release, error) {
	scope := scopeOf(ref, kind)
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, opened := r.opened[scope]; opened {
		return existing, nil
	}
	cfg, err := r.resolve(ctx, scope)
	if err != nil {
		return nil, err
	}
	plugin, err := r.assetSetPlugin()
	if err != nil {
		return nil, err
	}
	created := &release{Stacks: r, cfg: cfg}
	created.automation = pulumi.New(pulumi.Config{
		Backend: pulumi.Backend{
			URL:        cfg.BackendURL,
			Passphrase: cfg.Passphrase,
			Project:    cfg.PulumiProject,
			Env:        map[string]string{"AWS_REGION": cfg.Region},
		},
		Program:   created.Run,
		Configure: created.Configure,
		Secrets:   created.Secrets,
		Decode:    created.Decode,
		Refresh:   refreshPolicy(r.realized),
		Engine:    r.engine,
		Plugins:   []pulumi.Plugin{plugin},
	})
	r.opened[scope] = created
	return created, nil
}

func Serves() []provider.BindingType {
	return []provider.BindingType{provider.BindingPostgres, provider.BindingBucket, provider.BindingKV, provider.BindingTask, provider.BindingTopic, provider.BindingRealtime}
}

const skipTeardownRefreshEnv = "OCEL_SKIP_TEARDOWN_REFRESH"

func skipTeardownRefresh() bool {
	switch strings.ToLower(os.Getenv(skipTeardownRefreshEnv)) {
	case "1", "true":
		return true
	}
	return false
}

func refreshPolicy(realized *Realized) func(provider.StackRef, pulumi.Operation) bool {
	return func(ref provider.StackRef, op pulumi.Operation) bool {
		if op != pulumi.OperationDestroy || skipTeardownRefresh() {
			return false
		}
		return !realized.realizedHere(naming.Sanitize(ref.Project), ref.Name)
	}
}

type stackWork struct {
	program sdk.RunFunc
	tags    map[string]string
	outputs auto.OutputMap
}

type infraWork struct {
	transformed *transformPatches
	completer   payloads.Placement
	authorizer  payloads.Placement
	previewing  bool
}

func (r *release) Run(pctx *sdk.Context, spec provider.StackSpec) error {
	shipped, err := r.shipArtifacts(pctx, spec.Uploads)
	if err != nil {
		return err
	}
	switch work := spec.VendorState.(type) {
	case *stackWork:
		return work.program(pctx)
	case *appWork:
		if err := work.transformed.install(pctx); err != nil {
			return err
		}
		return work.run(pctx, shipped)
	case *containerWork:
		return work.run(pctx)
	case *containerInfraWork:
		return work.run(pctx)
	case *workersWork:
		return work.run(pctx)
	case *infraWork:
		if err := work.transformed.install(pctx); err != nil {
			return err
		}
		return r.infra(pctx, spec, work)
	}
	if spec.Kind != provider.StackInfra {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s provisions an app and this spec names none", spec.Ref.Name)
	}
	return r.infra(pctx, spec, &infraWork{})
}

func (r *release) infra(pctx *sdk.Context, spec provider.StackSpec, work *infraWork) error {
	vpc, err := ec2.LookupVpc(pctx, &ec2.LookupVpcArgs{Default: sdk.BoolRef(true)})
	if err != nil {
		return fmt.Errorf("look up default VPC: %w", err)
	}
	subnets, err := ec2.GetSubnets(pctx, &ec2.GetSubnetsArgs{
		Filters: []ec2.GetSubnetsFilter{{Name: "vpc-id", Values: []string{vpc.Id}}},
	})
	if err != nil {
		return fmt.Errorf("look up default VPC subnets: %w", err)
	}

	project, env := naming.Sanitize(spec.Ref.Project), spec.Ref.Name.Env
	transformed := work.transformed
	sessions := newSessionScope(project, env, r.cfg.StateTableARN)

	for _, resource := range spec.Resources {
		if resource.Binding != "" {
			continue
		}
		var err error
		switch resource.Type {
		case provider.BindingPostgres:
			args := translatePostgres(resource.Postgres)
			args.Tags = transformed.tagsFor(transformTypePostgres, resource.Name)
			err = registerPostgres(pctx, project, env, resource.Name, args, vpc.Id, vpc.CidrBlock, subnets.Ids)
		case provider.BindingBucket:
			args := translateBucket(resource.Bucket)
			args.Tags = transformed.tagsFor(transformTypeBucket, resource.Name)
			args.PatchedCORS = transformed.opensCORS(resource.Name)
			err = registerBucket(pctx, project, env, resource.Name, args, r.cfg.StateTable, r.cfg.AppBoundaryARN, r.cfg.AppRolePath, sessions, work.completer)
		case provider.BindingKV:
			err = r.declareKV(pctx, project, env, resource, work, vpc.Id, vpc.CidrBlock, subnets.Ids)
		case provider.BindingTask, provider.BindingTopic, provider.BindingRealtime:
			continue
		default:
			return refusal.Refuse(refusal.CodeInvalid,
				"this provider provisions no %s; it provisions %s, %s, %s, %s, %s and %s", resource.Type,
				provider.BindingPostgres, provider.BindingBucket, provider.BindingKV, provider.BindingTask, provider.BindingTopic, provider.BindingRealtime)
		}
		if err != nil {
			return fmt.Errorf("declare %s: %w", resource.Name, err)
		}
	}
	if err := r.declareRealtime(pctx, project, env, spec.Resources, work); err != nil {
		return err
	}
	return r.declareTopics(pctx, project, env, spec.Resources)
}

func (r *release) declareTopics(pctx *sdk.Context, project, env string, resources []provider.Resource) error {
	topics := topicsOf(project, env, resources)
	if scheduleGroupNeeded(topics) {
		if _, err := scheduler.NewScheduleGroup(pctx, naming.ResourceID(naming.KindWorker, "cron"), &scheduler.ScheduleGroupArgs{
			Name: sdk.String(queues.ScheduleGroupName(project, env)),
		}); err != nil {
			return err
		}
	}
	visibility := func(worker string) int { return queueVisibilitySeconds(topics, worker) }
	for _, topic := range topics {
		if err := registerTopic(pctx, topic, visibility, resourceTags(naming.KindTopic, "", map[string]string{tagResource: topic.resource.Declared})); err != nil {
			return fmt.Errorf("declare %s: %w", topic.resource.Declared, err)
		}
	}
	return nil
}

func provisionsBucket(spec provider.StackSpec) bool {
	return slices.ContainsFunc(spec.Resources, func(resource provider.Resource) bool {
		return resource.Type == provider.BindingBucket && resource.Binding == ""
	})
}

func (r *release) Configure(_ context.Context, spec provider.StackSpec) (auto.ConfigMap, error) {
	tags := spec.Tags
	if work, ok := spec.VendorState.(*stackWork); ok {
		tags = work.tags
	}
	if len(tags) == 0 {
		return auto.ConfigMap{}, nil
	}
	encoded, err := json.Marshal(map[string]map[string]string{"tags": tags})
	if err != nil {
		return nil, fmt.Errorf("render default tags: %w", err)
	}
	return auto.ConfigMap{"aws:defaultTags": auto.ConfigValue{Value: string(encoded)}}, nil
}

func (r *release) Secrets(spec provider.StackSpec) []string {
	secrets := []string{r.cfg.OriginSecret, r.cfg.PreviousOriginSecret, r.cfg.ISRWriterSeed}
	if r.cfg.ISRWriterSeed != "" && spec.App != nil && spec.App.ISR != nil {
		secrets = append(secrets, cloudflare.DeriveISRWriteSecret(r.cfg.ISRWriterSeed, spec.App.ISR.Prefix))
	}
	return secrets
}

func (r *release) Decode(ctx context.Context, spec provider.StackSpec, outputs auto.OutputMap) (provider.StackResult, error) {
	if work, ok := spec.VendorState.(*stackWork); ok {
		work.outputs = outputs
		return provider.StackResult{}, nil
	}
	if work, ok := spec.VendorState.(*containerInfraWork); ok {
		work.outputs = outputs
		return provider.StackResult{}, nil
	}
	if work, ok := spec.VendorState.(*workersWork); ok {
		return work.decode(outputs)
	}
	if work, ok := spec.VendorState.(*containerWork); ok {
		return r.decodeContainer(work, outputs)
	}
	if spec.App != nil {
		return r.decodeApp(spec, outputs)
	}
	sessions := newSessionScope(naming.Sanitize(spec.Ref.Project), spec.Ref.Name.Env, r.cfg.StateTableARN)
	result := provider.StackResult{}
	for _, resource := range spec.Resources {
		if resource.Binding != "" {
			continue
		}
		if resource.Type == provider.BindingTask || resource.Type == provider.BindingTopic {
			binding, err := collectTopicBinding(r.cfg, naming.Sanitize(spec.Ref.Project), spec.Ref.Name.Env, resource)
			if err != nil {
				return provider.StackResult{}, err
			}
			collected := bindingOf(resource.Type, binding)
			collected.Resource = resource.Declared
			result.Bindings = append(result.Bindings, collected)
			continue
		}
		fields, err := requireOutputFields(outputs, resource.Name)
		if err != nil {
			return provider.StackResult{}, err
		}
		var binding *bindingsv1.Binding
		switch resource.Type {
		case provider.BindingPostgres:
			binding, err = collectPostgresBinding(ctx, r.cfg.Secrets, resource.Name, fields)
		case provider.BindingBucket:
			binding, err = collectBucketBinding(resource.Name, sessions, fields)
		case provider.BindingKV:
			binding, err = collectKVBinding(ctx, r.cfg.Parameters, resource.Name, fields)
		case provider.BindingRealtime:
			binding, err = collectRealtimeBinding(ctx, r.cfg.SigningKeys, resource.Name, fields)
		}
		if err != nil {
			return provider.StackResult{}, err
		}
		collected := bindingOf(resource.Type, binding)
		collected.Resource = resource.Declared
		result.Bindings = append(result.Bindings, collected)
	}
	return result, nil
}

func bindingOf(kind provider.BindingType, binding *bindingsv1.Binding) provider.Binding {
	properties := map[string]string{}
	switch kind {
	case provider.BindingPostgres:
		p := binding.GetPostgres()
		properties[provider.PropertyHost] = p.GetHost()
		properties[provider.PropertyPort] = strconv.Itoa(int(p.GetPort()))
		properties[provider.PropertyDatabase] = p.GetDatabase()
		properties[provider.PropertyUsername] = p.GetUsername()
		properties[provider.PropertyPassword] = p.GetPassword()
	case provider.BindingBucket:
		properties[provider.PropertyBucket] = binding.GetBucket().GetBucket()
	case provider.BindingKV:
		kv := binding.GetKv()
		properties[provider.PropertyHost] = kv.GetHost()
		properties[provider.PropertyPort] = strconv.Itoa(int(kv.GetPort()))
		properties[provider.PropertyUsername] = kv.GetUsername()
		properties[provider.PropertyPassword] = kv.GetPassword()
		properties[provider.PropertyTLS] = strconv.FormatBool(kv.GetTls())
	case provider.BindingRealtime:
		realtime := binding.GetRealtime()
		properties[provider.PropertyTransport] = realtime.GetTransport().String()
		properties[provider.PropertyURL] = realtime.GetUrl()
		properties[provider.PropertyHost] = realtime.GetHost()
		properties[provider.PropertySigningKey] = base64.StdEncoding.EncodeToString(realtime.GetSigningKey())
		properties[provider.PropertyVerifyKey] = base64.StdEncoding.EncodeToString(realtime.GetVerifyKey())
	}
	return provider.Binding{
		Type:       kind,
		Name:       binding.GetName(),
		Properties: properties,
		Grants:     provider.GrantsOf(binding),
	}
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

func (r *Stacks) PackApp(ctx context.Context, req provider.PackAppRequest, _ progress.Log) (provider.PackAppResult, error) {
	opened, err := r.at(ctx, req.Ref, req.Edge)
	if err != nil {
		return provider.PackAppResult{}, err
	}
	bundle, err := opened.sealApp(req.Ref.Project, req.App, req.Values)
	if err != nil {
		return provider.PackAppResult{}, err
	}
	topology, err := queueTopology(opened.cfg, naming.Sanitize(req.Ref.Project), req.Ref.Name.Env, req.Topics, nil)
	if err != nil {
		return provider.PackAppResult{}, err
	}
	if bundle.Queues, err = queues.Render(topology); err != nil {
		return provider.PackAppResult{}, err
	}
	return provider.PackAppResult{Overlay: bundle.overlay(), VendorState: bundle}, nil
}

func (r *Stacks) Plan(ctx context.Context, spec provider.StackSpec, progress progress.Log) (provider.Plan, error) {
	opened, err := r.at(ctx, spec.Ref, edgeKindOf(spec))
	if err != nil {
		return provider.Plan{}, err
	}
	return opened.plan(ctx, spec, progress)
}

func (r *Stacks) PlanDestroy(ctx context.Context, ref provider.StackRef, progress progress.Log) (provider.Plan, error) {
	opened, err := r.at(ctx, ref, "")
	if err != nil {
		return provider.Plan{}, err
	}
	return opened.automation.PreviewDestroy(ctx, ref, progress)
}

func (r *Stacks) Provision(ctx context.Context, spec provider.StackSpec, progress progress.Log) (provider.StackResult, error) {
	opened, err := r.at(ctx, spec.Ref, edgeKindOf(spec))
	if err != nil {
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
	prepared, work, err := r.prepare(ctx, spec, runProvision)
	if err != nil {
		return provider.StackResult{}, err
	}
	if work != nil && len(work.sets) > 0 {
		r.pending.add(work.stack, work.sets, progress)
		defer r.pending.drop(work.stack, work.sets)
	}
	var listedTokens, listedKeys []string
	if spec.App == nil {
		if listedTokens, err = r.listKVTokens(ctx, spec.Ref); err != nil {
			return provider.StackResult{}, err
		}
		if listedKeys, err = r.listSigningKeys(ctx, spec.Ref); err != nil {
			return provider.StackResult{}, err
		}
	}
	result, err := r.automation.Run(ctx, prepared, progress)
	if err != nil {
		return provider.StackResult{}, err
	}
	if err := transformedIn(prepared).refuseUnclaimed(); err != nil {
		return provider.StackResult{}, err
	}
	if err := deleteKVTokens(ctx, r.cfg.Parameters, r.findUndeclaredKVTokens(listedTokens, spec)); err != nil {
		return provider.StackResult{}, err
	}
	if err := deleteSigningKeys(ctx, r.cfg.SigningKeys, r.findUndeclaredSigningKeys(listedKeys, spec)); err != nil {
		return provider.StackResult{}, err
	}
	if err := writeOriginRecord(ctx, r.cfg, spec.Ref.Name.App, work, result); err != nil {
		return provider.StackResult{}, err
	}
	workers, err := r.provisionWorkers(ctx, prepared, work, progress)
	if err != nil {
		return provider.StackResult{}, err
	}
	result.Functions = append(result.Functions, workers...)
	return result, nil
}

func (r *release) plan(ctx context.Context, spec provider.StackSpec, progress progress.Log) (provider.Plan, error) {
	if runsContainer(spec) {
		return r.planContainer(ctx, spec, progress)
	}
	prepared, _, err := r.prepare(ctx, spec, runPreview)
	if err != nil {
		return provider.Plan{}, err
	}
	previewed, err := r.automation.Preview(ctx, prepared, progress)
	if err != nil {
		return provider.Plan{}, err
	}
	if err := transformedIn(prepared).refuseUnclaimed(); err != nil {
		return provider.Plan{}, err
	}
	return previewed, nil
}

func transformedIn(spec provider.StackSpec) *transformPatches {
	switch work := spec.VendorState.(type) {
	case *appWork:
		return work.transformed
	case *infraWork:
		return work.transformed
	}
	return nil
}

type runKind int

const (
	runProvision runKind = iota
	runPreview
)

func (r *release) prepare(ctx context.Context, spec provider.StackSpec, kind runKind) (provider.StackSpec, *appWork, error) {
	if spec.VendorState != nil {
		return spec, nil, nil
	}
	if len(spec.Images.Pushes) > 0 {
		return provider.StackSpec{}, nil, refusal.Refuse(refusal.CodeInvalid,
			"%s runs on serverless compute, which runs functions rather than an image, and this release pushes %d", spec.Ref.Name, len(spec.Images.Pushes))
	}
	transformed, err := transformStackSpec(ctx, r.cfg.Transform, spec)
	if err != nil {
		return provider.StackSpec{}, nil, err
	}
	if spec.App == nil {
		if err := r.refuseHandover(ctx, spec); err != nil {
			return provider.StackSpec{}, nil, err
		}
		work := &infraWork{transformed: transformed, previewing: kind == runPreview}
		if provisionsBucket(spec) {
			if work.completer, err = placeUploadCompleter(ctx, r.cfg); err != nil {
				return provider.StackSpec{}, nil, err
			}
		}
		if provisionsRealtime(spec) {
			if work.authorizer, err = placeRealtimeAuthorizer(ctx, r.cfg); err != nil {
				return provider.StackSpec{}, nil, err
			}
		}
		spec.VendorState = work
		return spec, nil, nil
	}
	work, err := r.appWork(spec, transformed)
	if err != nil {
		return provider.StackSpec{}, nil, err
	}
	spec.VendorState = work
	return spec, work, nil
}

func (r *Stacks) Destroy(ctx context.Context, ref provider.StackRef, images provider.ImageStore, progress progress.Log) error {
	opened, err := r.at(ctx, ref, "")
	if err != nil {
		return err
	}
	if err := r.destroyWorkers(ctx, opened, ref, progress); err != nil {
		return err
	}
	if err := opened.automation.Destroy(ctx, ref, progress); err != nil {
		return err
	}
	if ref.Name.IsInfra() {
		tokens, err := opened.listKVTokens(ctx, ref)
		if err != nil {
			return err
		}
		if err := deleteKVTokens(ctx, opened.cfg.Parameters, tokens); err != nil {
			return err
		}
		keys, err := opened.listSigningKeys(ctx, ref)
		if err != nil {
			return err
		}
		if err := deleteSigningKeys(ctx, opened.cfg.SigningKeys, keys); err != nil {
			return err
		}
	}
	if opened.cfg.Tags != nil {
		if err := opened.cfg.Tags.Sweep(ctx, naming.Sanitize(ref.Project), ref.Name); err != nil {
			return err
		}
	}
	if err := r.releaseContainerInfra(ctx, opened.cfg.KeyValues, ref, progress); err != nil {
		return err
	}
	if ref.Name.IsInfra() {
		return nil
	}
	return resources.ForgetReleases(ctx, opened.cfg.Retention, ref, ref.Name.App, images, progress)
}

func (r *Stacks) Inspect(ctx context.Context, ref provider.StackRef) (provider.InspectedStack, error) {
	outputs, err := r.Outputs(ctx, ref, nil)
	if err != nil {
		return provider.InspectedStack{}, err
	}
	return provider.InspectedStack{Present: len(outputs) > 0}, nil
}

func (r *Stacks) Outputs(ctx context.Context, ref provider.StackRef, progress progress.Log) (auto.OutputMap, error) {
	opened, err := r.at(ctx, ref, "")
	if err != nil {
		return nil, err
	}
	return opened.automation.Outputs(ctx, ref, progress)
}

var _ provider.Stacks = (*Stacks)(nil)
