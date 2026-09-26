package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	ec2 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	kitpulumi "github.com/ocelhq/ocel/pkg/providerkit/pulumi"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Scope struct {
	Class edge.Class
	Slug  string
	Env   string
	Edge  edge.Kind
}

func scopeOf(ref provider.StackRef, kind edge.Kind) Scope {
	return Scope{Class: ref.Class, Slug: ref.Project, Env: ref.Name.Env, Edge: kind}
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
	engine   kitpulumi.Engine

	served *servedApps

	pending    *pendingSets
	pluginOnce sync.Once
	plugin     kitpulumi.Plugin
	pluginErr  error

	mu     sync.Mutex
	opened map[Scope]*release

	containerInfraLock sync.Mutex
}

type release struct {
	*Stacks
	cfg        Config
	automation *kitpulumi.Automation
}

func NewStacks(resolve ReleaseConfig, realized *Realized) *Stacks {
	return newStacks(resolve, realized, nil)
}

func newStacks(resolve ReleaseConfig, realized *Realized, engine kitpulumi.Engine) *Stacks {
	return &Stacks{
		resolve:  resolve,
		realized: realized,
		engine:   engine,
		served:   newServedApps(),
		pending:  newPendingSets(),
		opened:   map[Scope]*release{},
	}
}

func (r *Stacks) assetSetPlugin() (kitpulumi.Plugin, error) {
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
	created.automation = kitpulumi.New(kitpulumi.Config{
		Backend: kitpulumi.Backend{
			URL:        cfg.BackendURL,
			Passphrase: cfg.Passphrase,
			Project:    cfg.PulumiProject,
			Env:        map[string]string{"AWS_REGION": cfg.Region},
		},
		Program:   created.Run,
		Configure: created.Configure,
		Decode:    created.Decode,
		Refresh:   refreshPolicy(r.realized),
		Engine:    r.engine,
		Plugins:   []kitpulumi.Plugin{plugin},
	})
	r.opened[scope] = created
	return created, nil
}

func Serves() []provider.BindingType {
	return []provider.BindingType{provider.BindingPostgres, provider.BindingBucket}
}

const skipTeardownRefreshEnv = "OCEL_SKIP_TEARDOWN_REFRESH"

func skipTeardownRefresh() bool {
	switch strings.ToLower(os.Getenv(skipTeardownRefreshEnv)) {
	case "1", "true":
		return true
	}
	return false
}

func refreshPolicy(realized *Realized) func(provider.StackRef, kitpulumi.Operation) bool {
	return func(ref provider.StackRef, op kitpulumi.Operation) bool {
		if op != kitpulumi.OperationDestroy || skipTeardownRefresh() {
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
			err = registerBucket(pctx, project, env, resource.Name, args, r.cfg.StateTable, r.cfg.AppBoundaryARN, sessions, work.completer)
		default:
			return refusal.Refuse(refusal.CodeInvalid,
				"this provider provisions no %s; it provisions %s and %s", resource.Type, provider.BindingPostgres, provider.BindingBucket)
		}
		if err != nil {
			return fmt.Errorf("declare %s: %w", resource.Name, err)
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

func (r *release) Decode(ctx context.Context, spec provider.StackSpec, outputs auto.OutputMap) (provider.StackResult, error) {
	if work, ok := spec.VendorState.(*stackWork); ok {
		work.outputs = outputs
		return provider.StackResult{}, nil
	}
	if work, ok := spec.VendorState.(*containerInfraWork); ok {
		work.outputs = outputs
		return provider.StackResult{}, nil
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
		raw, produced := outputs[resource.Name]
		if !produced {
			return provider.StackResult{}, fmt.Errorf("stack produced no output for %s", resource.Name)
		}
		fields, mapped := raw.Value.(map[string]any)
		if !mapped {
			return provider.StackResult{}, fmt.Errorf("output for %s is not a map", resource.Name)
		}
		var (
			binding *bindingsv1.Binding
			err     error
		)
		switch resource.Type {
		case provider.BindingPostgres:
			binding, err = collectPostgresBinding(ctx, r.cfg.Secrets, resource.Name, fields)
		case provider.BindingBucket:
			binding, err = collectBucketBinding(resource.Name, sessions, fields)
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

func (r *Stacks) PackApp(ctx context.Context, req provider.PackAppRequest, _ edge.Progress) (provider.PackAppResult, error) {
	opened, err := r.at(ctx, req.Ref, req.Edge)
	if err != nil {
		return provider.PackAppResult{}, err
	}
	bundle, err := opened.sealApp(req.Ref.Project, req.App, req.Values)
	if err != nil {
		return provider.PackAppResult{}, err
	}
	return provider.PackAppResult{Overlay: bundle.overlay(), VendorState: bundle}, nil
}

func (r *Stacks) Plan(ctx context.Context, spec provider.StackSpec, progress edge.Progress) (provider.Plan, error) {
	opened, err := r.at(ctx, spec.Ref, edgeKindOf(spec))
	if err != nil {
		return provider.Plan{}, err
	}
	return opened.plan(ctx, spec, progress)
}

func (r *Stacks) PlanDestroy(ctx context.Context, ref provider.StackRef, progress edge.Progress) (provider.Plan, error) {
	opened, err := r.at(ctx, ref, "")
	if err != nil {
		return provider.Plan{}, err
	}
	return opened.automation.PreviewDestroy(ctx, ref, progress)
}

func (r *Stacks) Provision(ctx context.Context, spec provider.StackSpec, progress edge.Progress) (provider.StackResult, error) {
	opened, err := r.at(ctx, spec.Ref, edgeKindOf(spec))
	if err != nil {
		return provider.StackResult{}, err
	}
	return opened.provision(ctx, spec, progress)
}

func (r *release) provision(ctx context.Context, spec provider.StackSpec, progress edge.Progress) (provider.StackResult, error) {
	r.realized.mark(naming.Sanitize(spec.Ref.Project), spec.Ref.Name)
	if runsContainer(spec) {
		return r.provisionContainer(ctx, spec, progress)
	}
	prepared, work, err := r.prepare(ctx, spec)
	if err != nil {
		return provider.StackResult{}, err
	}
	if work != nil && len(work.sets) > 0 {
		r.pending.add(work.stack, work.sets, progress)
		defer r.pending.drop(work.stack, work.sets)
	}
	result, err := r.automation.Run(ctx, prepared, progress)
	if err != nil {
		return provider.StackResult{}, err
	}
	if err := transformedIn(prepared).refuseUnclaimed(); err != nil {
		return provider.StackResult{}, err
	}
	if err := writeOriginRecord(ctx, r.cfg, spec.Ref.Name.App, work, result); err != nil {
		return provider.StackResult{}, err
	}
	return result, nil
}

func (r *release) plan(ctx context.Context, spec provider.StackSpec, progress edge.Progress) (provider.Plan, error) {
	if runsContainer(spec) {
		return r.planContainer(ctx, spec, progress)
	}
	prepared, _, err := r.prepare(ctx, spec)
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

func (r *release) prepare(ctx context.Context, spec provider.StackSpec) (provider.StackSpec, *appWork, error) {
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
		work := &infraWork{transformed: transformed}
		if provisionsBucket(spec) {
			if work.completer, err = placeUploadCompleter(ctx, r.cfg); err != nil {
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

func (r *Stacks) Destroy(ctx context.Context, ref provider.StackRef, progress edge.Progress) error {
	opened, err := r.at(ctx, ref, "")
	if err != nil {
		return err
	}
	if err := opened.automation.Destroy(ctx, ref, progress); err != nil {
		return err
	}
	if opened.cfg.Tags != nil {
		if err := opened.cfg.Tags.Sweep(ctx, naming.Sanitize(ref.Project), ref.Name); err != nil {
			return err
		}
	}
	return r.releaseContainerInfra(ctx, opened.cfg.Records, ref, progress)
}

func (r *Stacks) Inspect(ctx context.Context, ref provider.StackRef) (provider.InspectedStack, error) {
	outputs, err := r.Outputs(ctx, ref, nil)
	if err != nil {
		return provider.InspectedStack{}, err
	}
	return provider.InspectedStack{Present: len(outputs) > 0}, nil
}

func (r *Stacks) Outputs(ctx context.Context, ref provider.StackRef, progress edge.Progress) (auto.OutputMap, error) {
	opened, err := r.at(ctx, ref, "")
	if err != nil {
		return nil, err
	}
	return opened.automation.Outputs(ctx, ref, progress)
}

var _ provider.Stacks = (*Stacks)(nil)
