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
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	kitpulumi "github.com/ocelhq/ocel/pkg/providerkit/pulumi"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Scope struct {
	Class providerkit.Class
	Slug  string
	Env   string
	Edge  edge.Kind
}

func scopeOf(ref providerkit.StackRef, kind edge.Kind) Scope {
	return Scope{Class: ref.Class, Slug: ref.Project, Env: ref.Name.Env, Edge: kind}
}

func edgeKindOf(plan providerkit.StackPlan) edge.Kind {
	if plan.Edge == nil {
		return ""
	}
	return plan.Edge.Kind()
}

type Resolver interface {
	Release(ctx context.Context, scope Scope) (Config, error)
}

type ResolverFunc func(ctx context.Context, scope Scope) (Config, error)

func (f ResolverFunc) Release(ctx context.Context, scope Scope) (Config, error) { return f(ctx, scope) }

type Releaser struct {
	resolve  Resolver
	realized *Realized
	engine   kitpulumi.Engine

	served *servedApps

	pending    *pendingSets
	pluginOnce sync.Once
	plugin     kitpulumi.Plugin
	pluginErr  error

	mu     sync.Mutex
	opened map[Scope]*release
}

type release struct {
	*Releaser
	cfg     Config
	adapter *kitpulumi.Adapter
}

func NewReleaser(resolve Resolver, realized *Realized) *Releaser {
	return newReleaser(resolve, realized, nil)
}

func newReleaser(resolve Resolver, realized *Realized, engine kitpulumi.Engine) *Releaser {
	return &Releaser{
		resolve:  resolve,
		realized: realized,
		engine:   engine,
		served:   newServedApps(),
		pending:  newPendingSets(),
		opened:   map[Scope]*release{},
	}
}

func (r *Releaser) assetSetPlugin() (kitpulumi.Plugin, error) {
	r.pluginOnce.Do(func() { r.plugin, r.pluginErr = assetSetPlugin(r.pending) })
	return r.plugin, r.pluginErr
}

func (r *Releaser) at(ctx context.Context, ref providerkit.StackRef, kind edge.Kind) (*release, error) {
	scope := scopeOf(ref, kind)
	r.mu.Lock()
	defer r.mu.Unlock()
	if held, opened := r.opened[scope]; opened {
		return held, nil
	}
	cfg, err := r.resolve.Release(ctx, scope)
	if err != nil {
		return nil, err
	}
	plugin, err := r.assetSetPlugin()
	if err != nil {
		return nil, err
	}
	held := &release{Releaser: r, cfg: cfg}
	held.adapter = kitpulumi.New(kitpulumi.Config{
		Access: kitpulumi.Access{
			BackendURL: cfg.BackendURL,
			Passphrase: cfg.Passphrase,
			Project:    cfg.PulumiProject,
			Env:        map[string]string{"AWS_REGION": cfg.Region},
		},
		Program: held,
		Refresh: refreshPolicy(r.realized),
		Engine:  r.engine,
		Plugins: []kitpulumi.Plugin{plugin},
	})
	r.opened[scope] = held
	return held, nil
}

func Serves() []providerkit.LinkType {
	return []providerkit.LinkType{providerkit.LinkPostgres, providerkit.LinkBucket}
}

const skipTeardownRefreshEnv = "OCEL_SKIP_TEARDOWN_REFRESH"

func skipTeardownRefresh() bool {
	switch strings.ToLower(os.Getenv(skipTeardownRefreshEnv)) {
	case "1", "true":
		return true
	}
	return false
}

func refreshPolicy(realized *Realized) func(providerkit.StackRef, kitpulumi.Op) bool {
	return func(ref providerkit.StackRef, op kitpulumi.Op) bool {
		if op != kitpulumi.OpDestroy || skipTeardownRefresh() {
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
	transformed *transformedArgs
	completer   payloads.Placement
}

func (r *release) Run(pctx *sdk.Context, plan providerkit.StackPlan) error {
	shipped, err := r.shipArtifacts(pctx, plan.Uploads)
	if err != nil {
		return err
	}
	switch work := plan.Options.(type) {
	case *stackWork:
		return work.program(pctx)
	case *appWork:
		return work.run(pctx, shipped)
	case *infraWork:
		return r.infra(pctx, plan, work)
	}
	if plan.Kind != providerkit.StackInfra {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"%s stands up an app and this plan carries none", plan.Ref.Name)
	}
	return r.infra(pctx, plan, &infraWork{})
}

func (r *release) infra(pctx *sdk.Context, plan providerkit.StackPlan, work *infraWork) error {
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

	project, env := naming.Sanitize(plan.Ref.Project), plan.Ref.Name.Env
	transformed := work.transformed
	sessions := newSessionScope(project, env, r.cfg.StateTableARN)

	for _, resource := range plan.Resources {
		if resource.Linked {
			continue
		}
		var err error
		switch resource.Type {
		case providerkit.LinkPostgres:
			args := transformed.forPostgres(resource.Name, resource.Postgres)
			err = registerPostgres(pctx, project, env, resource.Name, args, vpc.Id, vpc.CidrBlock, subnets.Ids)
		case providerkit.LinkBucket:
			args := transformed.forBucket(resource.Name, resource.Bucket)
			err = registerBucket(pctx, project, env, resource.Name, args, r.cfg.StateTable, r.cfg.AppBoundaryARN, sessions, work.completer)
		default:
			return providerkit.Refuse(providerkit.CodeInvalid,
				"this provider stands up no %s; it stands up %s and %s", resource.Type, providerkit.LinkPostgres, providerkit.LinkBucket)
		}
		if err != nil {
			return fmt.Errorf("declare %s: %w", resource.Name, err)
		}
	}
	return nil
}

func provisionsBucket(plan providerkit.StackPlan) bool {
	return slices.ContainsFunc(plan.Resources, func(resource providerkit.Resource) bool {
		return resource.Type == providerkit.LinkBucket && !resource.Linked
	})
}

func (r *release) Configure(_ context.Context, plan providerkit.StackPlan) (auto.ConfigMap, error) {
	tags := plan.Tags
	if work, held := plan.Options.(*stackWork); held {
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

func (r *release) Decode(ctx context.Context, plan providerkit.StackPlan, outputs auto.OutputMap) (providerkit.StackResult, error) {
	if work, held := plan.Options.(*stackWork); held {
		work.outputs = outputs
		return providerkit.StackResult{}, nil
	}
	if plan.App != nil {
		return r.decodeApp(plan, outputs)
	}
	sessions := newSessionScope(naming.Sanitize(plan.Ref.Project), plan.Ref.Name.Env, r.cfg.StateTableARN)
	result := providerkit.StackResult{}
	for _, resource := range plan.Resources {
		if resource.Linked {
			continue
		}
		raw, produced := outputs[resource.Name]
		if !produced {
			return providerkit.StackResult{}, fmt.Errorf("stack produced no output for %s", resource.Name)
		}
		fields, mapped := raw.Value.(map[string]any)
		if !mapped {
			return providerkit.StackResult{}, fmt.Errorf("output for %s is not a map", resource.Name)
		}
		var (
			link *linksv1.Link
			err  error
		)
		switch resource.Type {
		case providerkit.LinkPostgres:
			link, err = collectPostgresLink(ctx, r.cfg.Secrets, resource.Name, fields)
		case providerkit.LinkBucket:
			link, err = collectBucketLink(resource.Name, sessions, fields)
		}
		if err != nil {
			return providerkit.StackResult{}, err
		}
		held := linkOf(resource.Type, link)
		held.Resource = resource.Declared
		result.Links = append(result.Links, held)
	}
	return result, nil
}

func linkOf(kind providerkit.LinkType, link *linksv1.Link) providerkit.Link {
	properties := map[string]string{}
	switch kind {
	case providerkit.LinkPostgres:
		p := link.GetPostgres()
		properties[providerkit.PropertyHost] = p.GetHost()
		properties[providerkit.PropertyPort] = strconv.Itoa(int(p.GetPort()))
		properties[providerkit.PropertyDatabase] = p.GetDatabase()
		properties[providerkit.PropertyUsername] = p.GetUsername()
		properties[providerkit.PropertyPassword] = p.GetPassword()
	case providerkit.LinkBucket:
		properties[providerkit.PropertyBucket] = link.GetBucket().GetBucket()
	}
	return providerkit.Link{
		Type:       kind,
		Name:       link.GetName(),
		Properties: properties,
		Grants:     providerkit.GrantsOf(link),
	}
}

func (r *release) refuseHandover(ctx context.Context, plan providerkit.StackPlan) error {
	var linked []providerkit.Resource
	for _, resource := range plan.Resources {
		if resource.Linked {
			linked = append(linked, resource)
		}
	}
	if len(linked) == 0 {
		return nil
	}
	outputs, err := r.adapter.Outputs(ctx, plan.Ref, nil)
	if err != nil {
		return err
	}
	var handed []string
	for _, resource := range linked {
		if _, provisioned := outputs[resource.Name]; provisioned {
			handed = append(handed, resource.Declared)
		}
	}
	if len(handed) == 0 {
		return nil
	}
	return &HandoverError{Links: handed, Stack: plan.Ref.Name.String()}
}

func (r *Releaser) PackApp(ctx context.Context, packing providerkit.AppPacking, _ providerkit.Reporter) (providerkit.AppPack, error) {
	held, err := r.at(ctx, packing.Ref, packing.Edge)
	if err != nil {
		return providerkit.AppPack{}, err
	}
	bundle, err := held.sealApp(packing.Ref.Project, packing.App, packing.Values)
	if err != nil {
		return providerkit.AppPack{}, err
	}
	return providerkit.AppPack{Overlay: bundle.overlay(), Carry: bundle}, nil
}

func (r *Releaser) Plan(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) (providerkit.Plan, error) {
	held, err := r.at(ctx, plan.Ref, edgeKindOf(plan))
	if err != nil {
		return providerkit.Plan{}, err
	}
	return held.plan(ctx, plan, report)
}

func (r *Releaser) PlanDestroy(ctx context.Context, ref providerkit.StackRef, report providerkit.Reporter) (providerkit.Plan, error) {
	held, err := r.at(ctx, ref, "")
	if err != nil {
		return providerkit.Plan{}, err
	}
	return held.adapter.PreviewDestroy(ctx, ref, report)
}

func (r *Releaser) Provision(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) (providerkit.StackResult, error) {
	held, err := r.at(ctx, plan.Ref, edgeKindOf(plan))
	if err != nil {
		return providerkit.StackResult{}, err
	}
	return held.provision(ctx, plan, report)
}

func (r *release) provision(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) (providerkit.StackResult, error) {
	r.realized.mark(naming.Sanitize(plan.Ref.Project), plan.Ref.Name)
	prepared, work, err := r.prepare(ctx, plan)
	if err != nil {
		return providerkit.StackResult{}, err
	}
	if work != nil && len(work.sets) > 0 {
		r.pending.hold(work.stack, work.sets, report)
		defer r.pending.drop(work.stack, work.sets)
	}
	return r.adapter.Run(ctx, prepared, report)
}

func (r *release) plan(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) (providerkit.Plan, error) {
	prepared, _, err := r.prepare(ctx, plan)
	if err != nil {
		return providerkit.Plan{}, err
	}
	return r.adapter.Preview(ctx, prepared, report)
}

func (r *release) prepare(ctx context.Context, plan providerkit.StackPlan) (providerkit.StackPlan, *appWork, error) {
	if plan.Options != nil {
		return plan, nil, nil
	}
	transformed, err := transformStackPlan(ctx, r.cfg.Transform, plan)
	if err != nil {
		return providerkit.StackPlan{}, nil, err
	}
	if plan.App == nil {
		if err := r.refuseHandover(ctx, plan); err != nil {
			return providerkit.StackPlan{}, nil, err
		}
		work := &infraWork{transformed: transformed}
		if provisionsBucket(plan) {
			if work.completer, err = placeUploadCompleter(ctx, r.cfg); err != nil {
				return providerkit.StackPlan{}, nil, err
			}
		}
		plan.Options = work
		return plan, nil, nil
	}
	work, err := r.appWork(plan, transformed)
	if err != nil {
		return providerkit.StackPlan{}, nil, err
	}
	plan.Options = work
	return plan, work, nil
}

func (r *Releaser) Destroy(ctx context.Context, ref providerkit.StackRef, report providerkit.Reporter) error {
	held, err := r.at(ctx, ref, "")
	if err != nil {
		return err
	}
	if err := held.adapter.Destroy(ctx, ref, report); err != nil {
		return err
	}
	if held.cfg.Tags == nil {
		return nil
	}
	return held.cfg.Tags.SweepTagClock(ctx, naming.Sanitize(ref.Project), ref.Name)
}

func (r *Releaser) Inspect(ctx context.Context, ref providerkit.StackRef) (providerkit.StackState, error) {
	outputs, err := r.Outputs(ctx, ref, nil)
	if err != nil {
		return providerkit.StackState{}, err
	}
	return providerkit.StackState{Present: len(outputs) > 0}, nil
}

func (r *Releaser) Outputs(ctx context.Context, ref providerkit.StackRef, report providerkit.Reporter) (auto.OutputMap, error) {
	held, err := r.at(ctx, ref, "")
	if err != nil {
		return nil, err
	}
	return held.adapter.Outputs(ctx, ref, report)
}

var _ providerkit.Releaser = (*Releaser)(nil)
