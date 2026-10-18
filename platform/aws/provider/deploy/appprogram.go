package deploy

import (
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/functioncode"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/container"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/function"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/bucket"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
	"github.com/ocelhq/ocel/platform/aws/provider/site"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/functionnetwork"
)

type appWork struct {
	transformed *transformPatches
	functions   function.AppStack
	role        function.ExecutionRole
	roleCoord   naming.Coordinate
	logical     []string
	bundle      variables.Bundle
	cache       *site.ISR
	sets        []site.AssetSet
	delivery    site.EdgeDelivery
	stack       string
	network     *functionnetwork.Network
	uploads     []functioncode.Upload

	networkPending  bool
	functionsAttach bool
}

func (w *appWork) run(pctx *sdk.Context, shipped map[string]sdk.Resource) error {
	pushed, err := site.DeclareAssetSets(pctx, w.stack, w.sets)
	if err != nil {
		return err
	}
	role, err := function.NewRole(pctx, w.roleCoord, w.role)
	if err != nil {
		return err
	}
	stack := w.functions
	stack.RoleArn = role.Arn
	stack.RoleName = role.Name
	stack.Shipped = shipped
	stack.Pushed = pushed
	return stack.Declare(pctx)
}

func (r *release) appWork(spec provider.StackSpec, transformed *transformPatches, network *functionnetwork.Network, bundle variables.Bundle, uploads []functioncode.Upload) (*appWork, error) {
	app := spec.App
	project, stack := naming.Sanitize(spec.Ref.Project), spec.Ref.Name
	sessions := bucket.NewSessions(project, stack.Env, r.cfg.StateTableARN)
	staged := functioncode.Refs(uploads)

	dispatch := function.NewDispatchHost(spec.App, r.cfg.Slug, r.cfg.AssetBucket, r.cfg.ImageOptimizerURL)
	guard, err := function.NewOriginGuard(spec.App, r.cfg.Tier, r.cfg.OriginSecret, r.cfg.PreviousOriginSecret)
	if err != nil {
		return nil, err
	}
	cache := r.isrCache(spec)
	bytecode := r.bytecodeCache(spec)
	policies, err := iam.NewBindingPolicies(app.Grants)
	if err != nil {
		return nil, err
	}
	if err := variables.CheckRuntimeOwnedNames(app.App, app.Values); err != nil {
		return nil, err
	}
	if err := site.CheckEdgeVariables(r.cfg.ArtifactRoot, app.App, app.Values, bundle); err != nil {
		return nil, err
	}

	functions := make([]function.AppFunction, 0, len(app.Functions))
	artifacts := make(map[string]function.Artifact, len(app.Functions))
	args := make(map[string]function.Args, len(app.Functions))
	logical := make([]string, 0, len(app.Functions))
	vpcAccess := false
	for _, spec := range app.Functions {
		declared, err := function.NewArgs(app.Framework, spec)
		if err != nil {
			return nil, err
		}
		declared.Tags = transformed.TagsFor(transformTypeFunction, spec.Name)
		if !transformed.placesInVPC(spec.Name) {
			declared.Network = network
		}
		args[spec.Name] = declared
		vpcAccess = vpcAccess || transformed.placesInVPC(spec.Name) || declared.Network != nil
		functions = append(functions, function.AppFunction{Logical: spec.Name, RouteID: spec.Route, HasWorkerEntry: spec.HasWorkerEntry})
		artifact, err := function.FindArtifact(r.cfg.buckets(), staged[spec.Name])
		if err != nil {
			return nil, fmt.Errorf("place %s's code: %w", spec.Name, err)
		}
		artifacts[spec.Name] = artifact
		logical = append(logical, spec.Name)
	}

	vpcAccess = vpcAccess || (network != nil && len(app.Workers) > 0)

	appFunctions := function.AppStack{
		Project:   project,
		Stack:     stack,
		Functions: functions,
		Args:      func(fn function.AppFunction) function.Args { return args[fn.Logical] },
		Artifacts: artifacts,
		Env:       function.AppEnv(spec, bundle, sessions, r.cfg.StateTable),
		ISR:       cache,
		Bytecode:  bytecode,
		Dispatch:  dispatch,
		Guard:     guard,
		KmsKeyARN: r.cfg.VariablesKeyARN,
	}
	if err := appFunctions.CheckEnvBudget(); err != nil {
		return nil, err
	}

	if appFunctions.Layers, err = r.runtimeLayers(args); err != nil {
		return nil, err
	}

	var roleTags map[string]string
	if len(app.Functions) > 0 {
		roleTags = args[app.Functions[0].Name].Tags
	}
	role := function.ExecutionRole{
		App: app.App, Cache: cache, Bytecode: bytecode,
		Variables: variables.Reader{KeyARN: r.cfg.VariablesKeyARN}, Boundary: r.cfg.AppBoundaryARN, Path: r.cfg.AppRolePath,
		Tags: roleTags, BindingPolicies: policies, VPCAccess: vpcAccess, Dispatch: dispatch,
	}
	if topic.ReachesTasks(app) {
		wakes := container.WorkerServiceARNs(r.cfg.stateTable(), r.cfg.Tier, project, stack.Env, app.DeploymentWorkers)
		if role.TasksPolicy, err = topic.TasksPolicy(r.cfg.stateTable(), project, stack.Env, topic.Of(project, stack.Env, app.Topics), wakes); err != nil {
			return nil, err
		}
	}
	if bundle.HasLive() {
		role.Variables.TableARN = r.cfg.VariablesTableARN
		role.Variables.Referenced = bundle.Referenced
		role.Variables.Slug = r.cfg.Slug
		role.Variables.Tier = string(r.cfg.Tier)
	}

	r.served.Record(r.cfg.functionClients(), app.App, logical, bytecode)

	sets, delivery, err := site.ReadAssetSets(r.cfg.buckets(), r.cfg.ArtifactRoot, appCoordinate(spec), app, bundle, cache)
	if err != nil {
		return nil, err
	}

	return &appWork{
		uploads:     uploads,
		transformed: transformed,
		logical:     logical,
		bundle:      bundle,
		cache:       cache,
		sets:        sets,
		delivery:    delivery,
		stack:       stack.String(),
		network:     network,
		roleCoord:   component.AppRoleCoordinate(project, stack),
		role:        role,
		functions:   appFunctions,
	}, nil
}

func (r *release) runtimeLayers(args map[string]function.Args) (map[string]string, error) {
	layers := map[string]string{}
	for _, declared := range args {
		arch := declared.Arch
		if _, seen := layers[arch]; seen {
			continue
		}
		arn := r.cfg.RuntimeLayers[arch]
		if arn == "" {
			return nil, refusal.Refuse(refusal.CodeNotReady,
				"this account's bootstrap publishes no %s runtime for this build's functions to boot through; re-run `%s`",
				arch, provider.BootstrapCommand(r.cfg.Tier))
		}
		layers[arch] = arn
	}
	return layers, nil
}

func (r *release) isrCache(spec provider.StackSpec) *site.ISR {
	isr := spec.App.ISR
	if isr == nil {
		return nil
	}
	cache := &site.ISR{
		Coord:     appCoordinate(spec),
		Namespace: isr.TagNamespace,
		Bucket:    r.cfg.AssetBucket,
		Prefix:    isr.Prefix,
		Table:     r.cfg.StateTable,
		TableARN:  r.cfg.StateTableARN,
	}
	if r.cfg.buckets().CanEdgeRead() {
		cache.EdgeReadable = true
		if r.cfg.PurgeSeed != "" {
			cache.PurgeSecret = edge.DerivePurgeSecret(r.cfg.PurgeSeed, isr.Prefix)
		}
	}
	return cache
}

func appCoordinate(spec provider.StackSpec) naming.Coordinate {
	stack := spec.Ref.Name
	return naming.Coordinate{
		Project: naming.Sanitize(spec.Ref.Project),
		Env:     stack.Env,
		App:     stack.App,
		Release: stack.Release,
	}
}

func (r *release) bytecodeCache(spec provider.StackSpec) *function.Bytecode {
	bytecode := spec.App.Bytecode
	if bytecode == nil {
		return nil
	}
	return &function.Bytecode{Bucket: r.cfg.AssetBucket, Prefix: bytecode.Prefix}
}

func (r *release) decodeApp(work *appWork, outputs auto.OutputMap) (provider.StackResult, error) {
	result := provider.StackResult{
		EdgeBundleKey: work.delivery.BundleKey,
		RouteTableKey: work.delivery.RouteTableKey,
		Envelope:      work.delivery.Envelope,
	}
	if work.cache != nil {
		result.PurgeSecret = work.cache.PurgeSecret
	}
	for _, logical := range work.logical {
		fields, err := component.RequireOutputFields(outputs, logical)
		if err != nil {
			return provider.StackResult{}, err
		}
		url, err := component.RequireStringField(fields, logical, function.OutputKeyURL)
		if err != nil {
			return provider.StackResult{}, err
		}
		fn := provider.Function{Name: logical, URL: url}
		if physical, named := fields[function.OutputKeyName].(string); named {
			fn.Physical = physical
			r.served.Realize(logical, physical)
		}
		result.Functions = append(result.Functions, fn)
	}
	return result, nil
}

func appExecutionRole(cfg Config, app string, caches map[string]*site.ISR, bytecode map[string]*function.Bytecode, bundle variables.Bundle, tags map[string]string, policies []iam.BindingPolicy, vpcAccess bool, dispatch *function.DispatchHost) function.ExecutionRole {
	role := function.ExecutionRole{App: app, Cache: caches[app], Bytecode: bytecode[app], Variables: variables.Reader{KeyARN: cfg.VariablesKeyARN}, Boundary: cfg.AppBoundaryARN, Path: cfg.AppRolePath, Tags: tags, BindingPolicies: policies, VPCAccess: vpcAccess, Dispatch: dispatch}
	if bundle.HasLive() {
		role.Variables.TableARN = cfg.VariablesTableARN
		role.Variables.Referenced = bundle.Referenced
		role.Variables.Slug = cfg.Slug
		role.Variables.Tier = string(cfg.Tier)
	}
	return role
}
