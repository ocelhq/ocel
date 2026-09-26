package deploy

import (
	"fmt"
	"maps"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type appWork struct {
	transformed *transformPatches
	functions   appStackFunctions
	role        executionRole
	roleCoord   naming.Coordinate
	logical     []string
	bundle      appBundle
	cache       *isrConfig
	sets        []assetSet
	delivery    edgeDelivery
	stack       string
}

func (w *appWork) run(pctx *sdk.Context, shipped map[string]sdk.Resource) error {
	pushed, err := declareAssetSets(pctx, w.stack, w.sets)
	if err != nil {
		return err
	}
	role, err := newFunctionRole(pctx, w.roleCoord, w.role)
	if err != nil {
		return err
	}
	stack := w.functions
	stack.RoleArn = role.Arn
	stack.RoleName = role.Name
	stack.Shipped = shipped
	stack.Pushed = pushed
	return stack.register(pctx)
}

func (r *release) appWork(spec provider.StackSpec, transformed *transformPatches) (*appWork, error) {
	app := spec.App
	project, stack := naming.Sanitize(spec.Ref.Project), spec.Ref.Name
	sessions := newSessionScope(project, stack.Env, r.cfg.StateTableARN)

	bundle, err := r.appBundle(spec)
	if err != nil {
		return nil, err
	}
	router, err := r.routerHost(spec)
	if err != nil {
		return nil, err
	}
	guard, err := r.originGuard(spec)
	if err != nil {
		return nil, err
	}
	cache := r.isrCache(spec)
	bytecode := r.bytecodeCache(spec)
	policies, err := planBindingPolicies(app.Grants)
	if err != nil {
		return nil, err
	}
	if err := checkRuntimeOwnedNames(app.App, app.Values.Plain); err != nil {
		return nil, err
	}
	if err := checkAppEdgeVariables(r.cfg, app.App, app.Values, bundle); err != nil {
		return nil, err
	}

	functions := make([]appFunction, 0, len(app.Functions))
	artifacts := make(map[string]artifactRef, len(app.Functions))
	args := make(map[string]functionArgs, len(app.Functions))
	logical := make([]string, 0, len(app.Functions))
	vpcAccess := false
	for _, spec := range app.Functions {
		declared, err := translateFunctionSpec(app.Framework, spec)
		if err != nil {
			return nil, err
		}
		declared.Tags = transformed.tagsFor(transformTypeFunction, spec.Name)
		args[spec.Name] = declared
		vpcAccess = vpcAccess || transformed.placesInVPC(spec.Name)
		functions = append(functions, appFunction{Logical: spec.Name, RouteID: spec.Route})
		artifact, err := r.artifactAt(spec.Artifact)
		if err != nil {
			return nil, fmt.Errorf("place %s's code: %w", spec.Name, err)
		}
		artifacts[spec.Name] = artifact
		logical = append(logical, spec.Name)
	}

	env := r.appEnv(spec, bundle, sessions)
	for i, fn := range functions {
		declared := env
		if router.hosts(fn) {
			declared = router.plannedEntryEnv(env, functions)
		}
		if guard.hosts(fn) {
			declared = guard.entryEnv(declared)
		}
		if err := checkFunctionEnvBudget(logical[i], functionEnv(declared, args[logical[i]], cache, bytecode)); err != nil {
			return nil, err
		}
	}

	layers, err := r.runtimeLayers(args)
	if err != nil {
		return nil, err
	}

	var roleTags map[string]string
	if len(app.Functions) > 0 {
		roleTags = args[app.Functions[0].Name].Tags
	}
	role := executionRole{
		App: app.App, Cache: cache, Bytecode: bytecode,
		VarsKeyARN: r.cfg.VarsKeyARN, Boundary: r.cfg.AppBoundaryARN,
		Tags: roleTags, BindingPolicies: policies, VPCAccess: vpcAccess, Router: router,
	}
	if bundle.hasLive() {
		role.ValuesTableARN = r.cfg.VarsTableARN
		role.VarsReferenced = bundle.Referenced
		role.Slug = r.cfg.Slug
		role.VarsClass = string(r.cfg.Class)
	}

	r.served.plan(r, app.App, logical, bytecode)

	sets, delivery, err := r.assetSets(spec, app.App, app.Framework, bundle, cache)
	if err != nil {
		return nil, err
	}

	return &appWork{
		transformed: transformed,
		logical:     logical,
		bundle:      bundle,
		cache:       cache,
		sets:        sets,
		delivery:    delivery,
		stack:       stack.String(),
		roleCoord:   roleCoordinate(project, stack),
		role:        role,
		functions: appStackFunctions{
			Project:   project,
			Stack:     stack,
			Functions: functions,
			Args:      func(fn appFunction) functionArgs { return args[fn.Logical] },
			Artifacts: artifacts,
			Env:       env,
			ISR:       cache,
			Bytecode:  bytecode,
			Router:    router,
			Guard:     guard,
			KmsKeyARN: r.cfg.VarsKeyARN,
			Layers:    layers,
		},
	}, nil
}

func (r *release) artifactAt(ref provider.ArtifactRef) (artifactRef, error) {
	bucket, err := r.store(ref.Bucket)
	if err != nil {
		return artifactRef{}, err
	}
	if ref.Key == "" {
		return artifactRef{}, fmt.Errorf("no code was uploaded for it")
	}
	return artifactRef{Bucket: bucket, Key: ref.Key}, nil
}

func (r *release) store(name string) (string, error) {
	switch name {
	case provider.StoreFunctions:
		return r.cfg.ArtifactBucket, nil
	case provider.StoreAssets:
		return r.cfg.AssetBucket, nil
	case provider.StoreCache:
		return r.cfg.CacheStoreBucket, nil
	}
	return "", fmt.Errorf("this provider keeps no %q store", name)
}

func (r *release) runtimeLayers(args map[string]functionArgs) (map[string]string, error) {
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
				arch, provider.BootstrapCommand(r.cfg.Class))
		}
		layers[arch] = arn
	}
	return layers, nil
}

func (r *release) routerHost(spec provider.StackSpec) (*routerHost, error) {
	routing := spec.App.Routing
	if routing == nil {
		return nil, nil
	}
	prefix := spec.App.AssetPrefix
	host := &routerHost{
		Entry:             routing.Entry,
		AssetBucket:       r.cfg.AssetBucket,
		AssetPrefix:       prefix,
		ImageOptimizerURL: r.cfg.ImageOptimizerURL,
		Env: map[string]string{
			routingManifestEnv: routingManifestInTask,
			assetPrefixEnv:     prefix,
			slugEnv:            r.cfg.Slug,
			appNameEnv:         spec.App.App,
			deploymentIDEnv:    spec.App.Deployment,
		},
	}
	if r.cfg.AssetBucket != "" {
		host.Env[assetBucketEnv] = r.cfg.AssetBucket
	}
	if r.cfg.ImageOptimizerURL != "" {
		host.Env[edge.ImageOptimizerURLVar] = r.cfg.ImageOptimizerURL
	}
	return host, nil
}

func (r *release) originGuard(spec provider.StackSpec) (*originGuard, error) {
	guard := spec.App.Guard
	if guard == nil {
		return nil, nil
	}
	if r.cfg.OriginSecret == "" {
		return nil, fmt.Errorf(
			"the edge reaches %s over a Function URL no signature guards, and this bootstrap has no secret for the entry function to demand of it; re-run `%s`",
			spec.App.App, provider.BootstrapCommand(r.cfg.Class))
	}
	return &originGuard{Entry: guard.Entry, Secret: r.cfg.OriginSecret, Previous: r.cfg.PreviousOriginSecret}, nil
}

func (r *release) isrCache(spec provider.StackSpec) *isrConfig {
	isr := spec.App.ISR
	if isr == nil {
		return nil
	}
	cache := &isrConfig{
		Coord:     appCoordinate(spec),
		Namespace: isr.TagNamespace,
		Bucket:    r.cfg.AssetBucket,
		Prefix:    isr.Prefix,
		Table:     r.cfg.StateTable,
		TableARN:  r.cfg.StateTableARN,
	}
	if isrEntriesAdopted(r.cfg.objectStores()) {
		cache.CacheStoreBucket = r.cfg.CacheStoreBucket
		cache.WriterURL = r.cfg.ISRWriterEndpoint + "/" + isr.Prefix + "/entry"
		cache.WriterSecret = isrWriteSecret(r.cfg.ISRWriterSeed, isr.Prefix)
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

func (r *release) bytecodeCache(spec provider.StackSpec) *bytecodeConfig {
	bytecode := spec.App.Bytecode
	if bytecode == nil {
		return nil
	}
	return &bytecodeConfig{Bucket: r.cfg.AssetBucket, Prefix: bytecode.Prefix}
}

func (r *release) appBundle(spec provider.StackSpec) (appBundle, error) {
	if sealed, ok := spec.App.VendorState.(appBundle); ok {
		return sealed, nil
	}
	return r.sealApp(spec.Ref.Project, spec.App.App, spec.App.Values)
}

func (r *release) sealApp(project, app string, values provider.AppValues) (appBundle, error) {
	bindings := make([]live.Binding, 0, len(values.Bindings))
	for _, binding := range values.Bindings {
		kind := provider.WireBindingType(binding.Type)
		bindings = append(bindings, live.Binding{
			Name:    binding.Name,
			Key:     naming.ResourceEnvName(kind, bindingResource(binding)),
			Type:    kind,
			Granted: binding.Version,
		})
	}
	keys := make([]live.Key, 0, len(values.Secrets))
	for _, secret := range values.Secrets {
		keys = append(keys, live.Key{Key: secret.Key, Folder: secret.Folder})
	}
	return sealAppBundle(r.cfg, project, app, values.Sensitive, keys, bindings)
}

func bindingResource(binding provider.Binding) string {
	if binding.Resource != "" {
		return binding.Resource
	}
	return binding.Name
}

func (r *release) appEnv(spec provider.StackSpec, bundle appBundle, sessions sessionScope) map[string]string {
	app := spec.App
	env := map[string]string{}
	if spec.Edge != nil {
		env[edgeKindEnv] = string(spec.Edge.Kind())
		facts := spec.Edge.Facts()
		if !facts.RunsCode {
			env[edge.OriginRouterVar] = "1"
			env[edge.OriginSignedVar] = "1"
		}
		if facts.InvalidatesByCacheTag {
			env[edge.CacheTagPurgeVar] = "1"
		}
	}
	if app.Proxied {
		env[envStateTable] = r.cfg.StateTable
		env[envSessionPrefix] = sessions.KeyPrefix
	}
	for key, value := range app.Values.Plain {
		env[key] = value
	}
	maps.Copy(env, app.Values.PhaseEnv())
	if app.Values.Folder != "" {
		env[constants.AppFolderEnvName] = app.Values.Folder
	}
	for key, value := range bundle.env() {
		env[key] = value
	}
	return env
}

func planBindingPolicies(grants []provider.Binding) ([]bindingPolicy, error) {
	out := make([]bindingPolicy, 0, len(grants))
	for _, binding := range grants {
		policy, err := bindingPolicyDocument(binding.Name, grantMessages(binding.Grants))
		if err != nil {
			return nil, err
		}
		if policy == "" {
			continue
		}
		out = append(out, bindingPolicy{Binding: binding.Name, Policy: policy})
	}
	return out, nil
}

func grantMessages(grants []provider.Grant) []*bindingsv1.Grant {
	if len(grants) == 0 {
		return nil
	}
	out := make([]*bindingsv1.Grant, 0, len(grants))
	for _, grant := range grants {
		message := &bindingsv1.Grant{Label: grant.Label, Actions: grant.Actions, Resources: grant.Resources}
		for _, condition := range grant.Conditions {
			message.Conditions = append(message.Conditions, &bindingsv1.GrantCondition{
				Operator: condition.Operator,
				Key:      condition.Key,
				Values:   condition.Values,
			})
		}
		out = append(out, message)
	}
	return out
}

func (r *release) decodeApp(spec provider.StackSpec, outputs auto.OutputMap) (provider.StackResult, error) {
	work, planned := spec.VendorState.(*appWork)
	if !planned {
		return provider.StackResult{}, fmt.Errorf("this stack was not planned as an app stack")
	}
	result := provider.StackResult{
		EdgeBundleKey: work.delivery.BundleKey,
		Envelope:      work.delivery.Envelope,
	}
	if work.cache != nil {
		result.ISRWriteSecret = work.cache.WriterSecret
	}
	for _, logical := range work.logical {
		raw, produced := outputs[logical]
		if !produced {
			return provider.StackResult{}, fmt.Errorf("stack produced no output for %s", logical)
		}
		fields, mapped := raw.Value.(map[string]any)
		if !mapped {
			return provider.StackResult{}, fmt.Errorf("output for %s is not a map", logical)
		}
		url, err := requireStringField(fields, logical, outputKeyFunctionURL)
		if err != nil {
			return provider.StackResult{}, err
		}
		fn := provider.Function{Name: logical, URL: url}
		if physical, named := fields[outputKeyFunctionName].(string); named {
			fn.Physical = physical
			r.served.realized(logical, physical)
		}
		result.Functions = append(result.Functions, fn)
	}
	return result, nil
}
