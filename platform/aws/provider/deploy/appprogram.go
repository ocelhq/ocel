package deploy

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/live"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type appWork struct {
	transformed *transformedArgs
	functions   appStackFunctions
	role        executionRole
	roleCoord   naming.Coordinate
	logical     []string
	bundle      appBundle
	cache       *isrConfig
}

func (w *appWork) run(pctx *sdk.Context) error {
	role, err := newFunctionRole(pctx, w.roleCoord, w.role)
	if err != nil {
		return err
	}
	stack := w.functions
	stack.RoleArn = role.Arn
	stack.RoleName = role.Name
	return stack.register(pctx)
}

func (r *release) appWork(plan providerkit.StackPlan, transformed *transformedArgs) (*appWork, error) {
	app := plan.App
	project, stack := naming.Sanitize(plan.Ref.Project), plan.Ref.Name
	sessions := newSessionScope(project, stack.Env, r.cfg.StateTableARN)

	bundle, err := r.appBundle(plan)
	if err != nil {
		return nil, err
	}
	router, err := r.routerHost(plan)
	if err != nil {
		return nil, err
	}
	guard, err := r.originGuard(plan)
	if err != nil {
		return nil, err
	}
	cache := r.isrCache(plan)
	bytecode := r.bytecodeCache(plan)
	policies, err := planLinkPolicies(app.Grants)
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
		declared := transformed.forSpec(app.Framework, spec)
		args[spec.Name] = declared
		vpcAccess = vpcAccess || declared.VPC.placed()
		functions = append(functions, appFunction{Logical: spec.Name, RouteID: spec.Route})
		held, err := r.artifactAt(spec.Artifact)
		if err != nil {
			return nil, fmt.Errorf("place %s's code: %w", spec.Name, err)
		}
		artifacts[spec.Name] = held
		logical = append(logical, spec.Name)
	}

	env := r.appEnv(plan, bundle, sessions)
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

	layer, err := r.membranePlacement(app.Membrane)
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
		Tags: roleTags, LinkPolicies: policies, VPCAccess: vpcAccess, Router: router,
	}
	if bundle.hasLive() {
		role.ValuesTableARN = r.cfg.StateTableARN
		role.VarsReferenced = bundle.Referenced
		role.Slug = r.cfg.Slug
		role.VarsClass = string(r.cfg.Class)
	}

	r.served.plan(r, app.App, logical, bytecode)

	return &appWork{
		transformed: transformed,
		logical:     logical,
		bundle:      bundle,
		cache:       cache,
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
			Layer:     layer,
		},
	}, nil
}

func (t *transformedArgs) forSpec(framework string, spec providerkit.FunctionSpec) functionArgs {
	if t != nil {
		if args, ok := t.functions[spec.Name]; ok {
			return args
		}
	}
	return translateFunctionSpec(framework, spec)
}

func (r *release) artifactAt(ref providerkit.ArtifactRef) (artifactRef, error) {
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
	case providerkit.StoreFunctions:
		return r.cfg.ArtifactBucket, nil
	case providerkit.StoreAssets:
		return r.cfg.AssetBucket, nil
	case providerkit.StoreCache:
		return r.cfg.CacheStoreBucket, nil
	}
	return "", fmt.Errorf("this provider keeps no %q store", name)
}

func (r *release) membranePlacement(ref providerkit.ArtifactRef) (payloads.Placement, error) {
	if ref.Key == "" {
		return payloads.Placement{}, nil
	}
	bucket, err := r.store(ref.Bucket)
	if err != nil {
		return payloads.Placement{}, fmt.Errorf("read the membrane: %w", err)
	}
	digest := strings.TrimSuffix(strings.TrimPrefix(ref.Key, providerkit.MembranePrefix+"/"), ".zip")
	return payloads.Placement{Bucket: bucket, Key: ref.Key, SHA256: digest}, nil
}

func (r *release) routerHost(plan providerkit.StackPlan) (*routerHost, error) {
	routing := plan.App.Routing
	if routing == nil {
		return nil, nil
	}
	prefix := plan.App.AssetPrefix
	host := &routerHost{
		Entry:             routing.Entry,
		Manifest:          routing.Manifest,
		AssetBucket:       r.cfg.AssetBucket,
		AssetPrefix:       prefix,
		ImageOptimizerURL: r.cfg.ImageOptimizerURL,
		Env: map[string]string{
			routingManifestEnv:         routingManifestInTask,
			assetPrefixEnv:             prefix,
			slugEnv:                    r.cfg.Slug,
			appNameEnv:                 plan.App.App,
			deploymentIDEnv:            plan.App.Deployment,
			edge.OriginBodyLimitVar:    strconv.Itoa(lambdaOriginBodyLimitBytes),
			edge.OriginBodyEncodingVar: edge.OriginBodyEncodingBase64,
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

func (r *release) originGuard(plan providerkit.StackPlan) (*originGuard, error) {
	guard := plan.App.Guard
	if guard == nil {
		return nil, nil
	}
	if r.cfg.OriginSecret == "" {
		return nil, fmt.Errorf(
			"the edge reaches %s over a Function URL no signature guards, and this bootstrap holds no secret for the entry function to demand of it; re-run `ocel bootstrap`",
			plan.App.App)
	}
	return &originGuard{Entry: guard.Entry, Secret: r.cfg.OriginSecret}, nil
}

func (r *release) isrCache(plan providerkit.StackPlan) *isrConfig {
	held := plan.App.ISR
	if held == nil {
		return nil
	}
	cache := &isrConfig{
		Coord:     appCoordinate(plan),
		Namespace: held.TagNamespace,
		Bucket:    r.cfg.AssetBucket,
		Prefix:    held.Prefix,
		Table:     r.cfg.StateTable,
		TableARN:  r.cfg.StateTableARN,
	}
	if isrEntriesAdopted(r.cfg.objectStores()) {
		cache.CacheStoreBucket = r.cfg.CacheStoreBucket
		cache.WriterURL = r.cfg.ISRWriterEndpoint + "/" + held.Prefix + "/entry"
		cache.WriterSecret = isrWriteSecret(r.cfg.ISRWriterSeed, held.Prefix)
	}
	return cache
}

func appCoordinate(plan providerkit.StackPlan) naming.Coordinate {
	stack := plan.Ref.Name
	return naming.Coordinate{
		Project: naming.Sanitize(plan.Ref.Project),
		Env:     stack.Env,
		App:     stack.App,
		Release: stack.Release,
	}
}

func (r *release) bytecodeCache(plan providerkit.StackPlan) *bytecodeConfig {
	held := plan.App.Bytecode
	if held == nil {
		return nil
	}
	return &bytecodeConfig{Bucket: r.cfg.AssetBucket, Prefix: held.Prefix}
}

func (r *release) appBundle(plan providerkit.StackPlan) (appBundle, error) {
	if sealed, carried := plan.App.Packed.(appBundle); carried {
		return sealed, nil
	}
	return r.sealApp(plan.Ref.Project, plan.App.App, plan.App.Values)
}

func (r *release) sealApp(project, app string, held providerkit.AppValues) (appBundle, error) {
	links := make([]live.Link, 0, len(held.Links))
	for _, link := range held.Links {
		kind := wireLinkType(link.Type)
		links = append(links, live.Link{
			Name:    link.Name,
			Key:     functionEnvKey(kind, linkResource(link)),
			Type:    kind,
			Granted: link.Version,
		})
	}
	keys := make([]live.Key, 0, len(held.Secrets))
	for _, secret := range held.Secrets {
		keys = append(keys, live.Key{Key: secret.Key, Folder: secret.Folder})
	}
	return sealAppBundle(r.cfg, project, app, held.Sensitive, keys, links)
}

func linkResource(link providerkit.Link) string {
	if link.Resource != "" {
		return link.Resource
	}
	return link.Name
}

func wireLinkType(kind providerkit.LinkType) linksv1.LinkType {
	switch kind {
	case providerkit.LinkPostgres:
		return linksv1.LinkType_LINK_TYPE_POSTGRES
	case providerkit.LinkBucket:
		return linksv1.LinkType_LINK_TYPE_BUCKET
	}
	return linksv1.LinkType_LINK_TYPE_CUSTOM
}

func (r *release) appEnv(plan providerkit.StackPlan, bundle appBundle, sessions sessionScope) map[string]string {
	app := plan.App
	env := map[string]string{}
	if plan.Edge != nil {
		env[edgeKindEnv] = string(plan.Edge.Kind())
		facts := plan.Edge.Facts()
		if !facts.RunsCode {
			env[edge.OriginRouterVar] = "1"
			env[edge.OriginSignedVar] = "1"
		}
		if facts.InvalidatesByCacheTag {
			env[edge.CacheTagPurgeVar] = "1"
		}
	}
	if app.CrossesMembrane {
		env[envStateTable] = r.cfg.StateTable
		env[envSessionPrefix] = sessions.KeyPrefix
	}
	for key, value := range app.Values.Plain {
		env[key] = value
	}
	if app.Values.Folder != "" {
		env[appFolderEnv] = app.Values.Folder
	}
	for key, value := range bundle.env() {
		env[key] = value
	}
	return env
}

func planLinkPolicies(grants []providerkit.Link) ([]linkPolicy, error) {
	out := make([]linkPolicy, 0, len(grants))
	for _, link := range grants {
		policy, err := linkPolicyDocument(link.Name, grantMessages(link.Grants))
		if err != nil {
			return nil, err
		}
		if policy == "" {
			continue
		}
		out = append(out, linkPolicy{Link: link.Name, Type: link.Type, Policy: policy})
	}
	return out, nil
}

func grantMessages(grants []providerkit.Grant) []*linksv1.Grant {
	if len(grants) == 0 {
		return nil
	}
	out := make([]*linksv1.Grant, 0, len(grants))
	for _, grant := range grants {
		message := &linksv1.Grant{Label: grant.Label, Actions: grant.Actions, Resources: grant.Resources}
		for _, condition := range grant.Conditions {
			message.Conditions = append(message.Conditions, &linksv1.GrantCondition{
				Operator: condition.Operator,
				Key:      condition.Key,
				Values:   condition.Values,
			})
		}
		out = append(out, message)
	}
	return out
}

func (r *release) decodeApp(plan providerkit.StackPlan, outputs auto.OutputMap) (providerkit.StackResult, error) {
	work, held := plan.Options.(*appWork)
	if !held {
		return providerkit.StackResult{}, fmt.Errorf("this stack was not planned as an app stack")
	}
	result := providerkit.StackResult{}
	for _, logical := range work.logical {
		raw, produced := outputs[logical]
		if !produced {
			return providerkit.StackResult{}, fmt.Errorf("stack produced no output for %s", logical)
		}
		fields, mapped := raw.Value.(map[string]any)
		if !mapped {
			return providerkit.StackResult{}, fmt.Errorf("output for %s is not a map", logical)
		}
		url, err := requireStringField(fields, logical, outputKeyFunctionURL)
		if err != nil {
			return providerkit.StackResult{}, err
		}
		fn := providerkit.Function{Name: logical, URL: url}
		if physical, named := fields[outputKeyFunctionName].(string); named {
			fn.Physical = physical
			r.served.realized(logical, physical)
		}
		result.Functions = append(result.Functions, fn)
	}
	return result, nil
}
