package function

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/processenv"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/functionurl"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/bucket"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

const routedManifest = `{"rootFunction":"/","buildId":"WEB1","basePath":"","pathnames":[],"routes":{},"dispatch":{}}`

func routedArtifactRoot(t *testing.T) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"apps/web/next-route-table.json": routedManifest,
		"apps/web/hosting.json":          `{"version":1,"framework":"next","frameworkBuildId":"WEB1","rootFunction":"/","routeTable":"next","static":{"immutablePrefixes":["/_next/static/"],"mustRevalidatePrefixes":["/_next/static/service-worker/"]}}`,
	})
}

func routedConfig(t *testing.T, kind edge.Kind) testConfig {
	t.Helper()
	return testConfig{
		ArtifactRoot:      routedArtifactRoot(t),
		EdgeBucket:        "edge-bucket",
		ImageOptimizerURL: "https://optimizer.lambda-url.us-east-1.on.aws/",
		Slug:              "shop",
		Edge:              fakeEdgeOf(kind),
	}
}

func plannedValues(app *contractv1.ManifestApp) provider.AppValues {
	values := provider.AppValues{Plain: map[string]string{}, Folder: app.GetFolder()}
	for _, v := range app.GetVariables() {
		if v.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN {
			values.Plain[v.GetKey()] = v.GetValue()
		}
	}
	return values
}

func plannedEnv(t *testing.T, cfg testConfig, app *contractv1.ManifestApp, front edge.Edge) map[string]string {
	t.Helper()
	spec := provider.StackSpec{
		Kind:     provider.StackApp,
		Recorded: true,
		Edge:     front,
		App:      &provider.AppSpec{App: app.GetName(), Values: plannedValues(app)},
	}
	return AppEnv(spec, variables.Bundle{}, bucket.Sessions{}, cfg.StateTable)
}

func routedCoordinate(t *testing.T) naming.Coordinate {
	t.Helper()
	return storageCoordinate("prod", "shop", "web", fixedRelease(t))
}

func routedApp() *contractv1.ManifestApp {
	return &contractv1.ManifestApp{Name: "web", Framework: &contractv1.Framework{Name: buildoutput.FrameworkNext}}
}

func servingSpec(t *testing.T, cfg testConfig, app, runtime string, coord naming.Coordinate) provider.StackSpec {
	t.Helper()
	stack := coord.Stack()
	facts := cfg.Edge.Facts()
	opened, err := edges.Routers{}.Open(router.Kind(cfg.Edge.Kind()))
	if err != nil {
		t.Fatalf("Routers().Open(%q): %v", cfg.Edge.Kind(), err)
	}
	serving, err := providerserver.AppServingFor(providerserver.AppServingInput{
		Root:              cfg.ArtifactRoot,
		Project:           "shop",
		App:               app,
		Framework:         runtime,
		Stack:             stack,
		Coordinate:        coord,
		EdgeRunsCode:      facts.RunsCode,
		EdgeSignsForwards: opened.Facts().SignsOriginForwards,
	})
	if err != nil {
		t.Fatalf("ServingFactsFor: %v", err)
	}
	return provider.StackSpec{
		Ref:      provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: stack},
		Kind:     provider.StackApp,
		Recorded: true,
		Edge:     cfg.Edge,
		App: &provider.AppSpec{
			App:       app,
			Framework: runtime,
			BuildID:   "d1",
			Routing:   serving.OriginDispatch,
			Guard:     serving.Guard,
			Storage:   coord,
			Static:    serving.Static,
		},
	}
}

func routedSpec(t *testing.T, cfg testConfig) provider.StackSpec {
	t.Helper()
	return servingSpec(t, cfg, "web", buildoutput.FrameworkNext, routedCoordinate(t))
}

func newDispatchHostFor(t *testing.T, cfg testConfig) *DispatchHost {
	t.Helper()
	return NewDispatchHost(routedSpec(t, cfg).App, cfg.Slug, cfg.EdgeBucket, cfg.ImageOptimizerURL)
}

func TestDispatchHostNamesTheEntryAndWhatDispatchReads(t *testing.T) {
	t.Parallel()

	cfg := routedConfig(t, cloudfront.Kind)
	host := newDispatchHostFor(t, cfg)
	if host == nil {
		t.Fatal("dispatch host = none, want the root function to host dispatch")
	}
	if host.RootFunction != "/" {
		t.Errorf("rootFunction = %q, want the route id the spec names", host.RootFunction)
	}
	want := map[string]string{
		routeTableEnv:             routeTableInTask,
		slugEnv:                   "shop",
		appNameEnv:                "web",
		"OCEL_BUILD_ID":           "d1",
		edge.ImageOptimizerURLVar: "https://optimizer.lambda-url.us-east-1.on.aws/",
	}
	for key, value := range want {
		if host.Env[key] != value {
			t.Errorf("entry env %s = %q, want %q", key, host.Env[key], value)
		}
	}
	if value, told := host.Env["OCEL_ASSET_PREFIX"]; told {
		t.Errorf("entry env OCEL_ASSET_PREFIX = %q, want none: the asset prefix derives from the storage coordinate", value)
	}
}

func TestTheRootFunctionReadsOnlyItsOwnReleasesAssetsOutOfTheEdgeBucket(t *testing.T) {
	t.Parallel()

	host := newDispatchHostFor(t, routedConfig(t, cloudfront.Kind))
	raw, err := assetReadPolicy(host)
	if err != nil {
		t.Fatalf("assetReadPolicy: %v", err)
	}
	var doc policyDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("policy is not valid JSON: %v", err)
	}
	want := []string{"arn:aws:s3:::edge-bucket/assets/shop/prod/web/r3f8a1c90/*"}
	if len(doc.Statement) != 1 || !slices.Equal(doc.Statement[0].Resource, want) || !slices.Equal(doc.Statement[0].Action, []string{"s3:GetObject"}) {
		t.Errorf("asset read policy = %s, want s3:GetObject on %v alone", raw, want)
	}
}

func routedFunctions() []*contractv1.ManifestFunction {
	return []*contractv1.ManifestFunction{
		{LogicalName: "fn--web--entry", Framework: &contractv1.Framework{Name: buildoutput.FrameworkNext}, RouteId: "/"},
		{LogicalName: "fn--web--admin", Framework: &contractv1.Framework{Name: buildoutput.FrameworkNext}, RouteId: "/admin"},
	}
}

func functionEnvOf(t *testing.T, rec *inputRecorder, name string) map[string]string {
	t.Helper()
	variables := rec.object(t, "aws:lambda/function:Function", name, "environment")["variables"]
	if !variables.IsObject() {
		t.Fatalf("function %s has no environment variables", name)
	}
	env := map[string]string{}
	for key, value := range variables.ObjectValue() {
		if value.IsString() {
			env[string(key)] = value.StringValue()
		}
	}
	return env
}

func TestEntryFunctionGetsTheEdgeKindAndItsSiblingURLs(t *testing.T) {
	t.Parallel()

	cfg := routedConfig(t, cloudfront.Kind)
	host := newDispatchHostFor(t, cfg)

	stack := testStack(t, "prod", "web")
	rec := &inputRecorder{}
	program := func(pctx *pulumi.Context) error {
		role, _, err := NewRole(pctx, component.AppRoleCoordinate("shop", stack), ExecutionRole{Namespace: testNamespace, Boundary: componenttest.BoundaryARN, Dispatch: host})
		if err != nil {
			return err
		}
		return AppStack{
			Namespace: testNamespace,
			Project:   "shop",
			Stack:     stack,
			Functions: manifestAppFunctions(routedFunctions()),
			Args:      argsFor(routedFunctions()),
			Artifacts: map[string]Artifact{
				"fn--web--entry": {Bucket: "artifacts", Key: "entry.zip"},
				"fn--web--admin": {Bucket: "artifacts", Key: "admin.zip"},
			},
			Layers:   testRuntimeLayers(),
			Env:      map[string]string{routerKindEnv: string(cloudfront.Kind)},
			Dispatch: host,
			RoleArn:  role.Arn,
			RoleName: role.Name,
		}.Declare(pctx)
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}

	entry := functionEnvOf(t, rec, namedFunction(stack, "fn--web--entry").Pulumi)
	if entry[routerKindEnv] != string(cloudfront.Kind) {
		t.Errorf("%s = %q, want the router kind the deploy chose", routerKindEnv, entry[routerKindEnv])
	}
	if entry[routeTableEnv] != routeTableInTask {
		t.Errorf("%s = %q, want the manifest packed beside the handler", routeTableEnv, entry[routeTableEnv])
	}
	var siblings map[string]string
	if err := json.Unmarshal([]byte(entry[functionURLsEnv]), &siblings); err != nil {
		t.Fatalf("%s = %q, want a routeId-to-URL object: %v", functionURLsEnv, entry[functionURLsEnv], err)
	}
	if len(siblings) != 1 || siblings["/admin"] == "" {
		t.Errorf("sibling urls = %v, want only the sibling bundle's Function URL", siblings)
	}
}

func TestASiblingFunctionHostsNoDispatch(t *testing.T) {
	t.Parallel()

	cfg := routedConfig(t, cloudfront.Kind)
	host := newDispatchHostFor(t, cfg)

	stack := testStack(t, "prod", "web")
	rec := &inputRecorder{}
	program := func(pctx *pulumi.Context) error {
		return AppStack{
			Namespace: testNamespace,
			Project:   "shop",
			Stack:     stack,
			Functions: manifestAppFunctions(routedFunctions()),
			Args:      argsFor(routedFunctions()),
			Artifacts: map[string]Artifact{},
			Layers:    testRuntimeLayers(),
			Env:       map[string]string{routerKindEnv: string(cloudfront.Kind)},
			Dispatch:  host,
			RoleArn:   pulumi.String("arn:aws:iam::123456789012:role/app"),
			RoleName:  pulumi.String("app"),
		}.Declare(pctx)
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}

	sibling := functionEnvOf(t, rec, namedFunction(stack, "fn--web--admin").Pulumi)
	if sibling[routerKindEnv] != string(cloudfront.Kind) {
		t.Errorf("%s = %q, want every function to learn the router kind", routerKindEnv, sibling[routerKindEnv])
	}
	for _, key := range []string{routeTableEnv, functionURLsEnv} {
		if _, wired := sibling[key]; wired {
			t.Errorf("sibling has %s, want dispatch wired into the root function alone", key)
		}
	}
}

func TestAppEnvPassesTheDeploymentURLToTheFunction(t *testing.T) {
	t.Parallel()

	app := routedApp()
	app.Variables = []*contractv1.ManifestVariable{
		{Key: processenv.AppURLEnvVar, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "https://shop.example"},
		{Key: processenv.NextPublicURLEnvVar, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "https://shop.example"},
	}

	env := plannedEnv(t, testConfig{}, app, nil)
	for _, key := range []string{processenv.AppURLEnvVar, processenv.NextPublicURLEnvVar} {
		if got, want := env[key], "https://shop.example"; got != want {
			t.Errorf("%s = %q, want %q: server code reads the url off its own environment", key, got, want)
		}
	}
}

func TestAnEdgeThatRunsNoCodeLeavesPathDispatchToTheOrigin(t *testing.T) {
	t.Parallel()

	behindCloudFront := plannedEnv(t, routedConfig(t, cloudfront.Kind), routedApp(), fakeEdgeOf(cloudfront.Kind))
	if behindCloudFront["OCEL_ORIGIN_DISPATCH"] != "1" {
		t.Errorf("OCEL_ORIGIN_DISPATCH = %q behind CloudFront, want \"1\": the root function dispatches each path", behindCloudFront["OCEL_ORIGIN_DISPATCH"])
	}

	behindCloudflare := plannedEnv(t, routedConfig(t, cloudflare.Kind), routedApp(), fakeEdgeOf(cloudflare.Kind))
	if _, dispatches := behindCloudflare["OCEL_ORIGIN_DISPATCH"]; dispatches {
		t.Error("OCEL_ORIGIN_DISPATCH is set behind Cloudflare, want it unset: the edge dispatches each path")
	}
}

func TestAnOriginIsToldWhichResponsesItsEdgeStores(t *testing.T) {
	t.Parallel()

	for kind, want := range map[edge.Kind]edge.EdgeCacheMode{cloudfront.Kind: edge.EdgeCacheAll, cloudflare.Kind: edge.EdgeCacheMarked} {
		env := plannedEnv(t, routedConfig(t, kind), routedApp(), fakeEdgeOf(kind))
		if got := edge.EdgeCacheMode(env[edge.EdgeCacheVar]); got != want {
			t.Errorf("%s = %q behind %s, want %q", edge.EdgeCacheVar, got, kind, want)
		}
	}

	if _, told := plannedEnv(t, testConfig{}, routedApp(), nil)[edge.EdgeCacheVar]; told {
		t.Errorf("%s is set with no edge, want it unset: nothing in front stores a response", edge.EdgeCacheVar)
	}
}

func TestAnOriginBehindACachingEdgeIsToldItsRelease(t *testing.T) {
	t.Parallel()

	token := naming.NewReleaseToken("b1", "f1")
	for _, kind := range []edge.Kind{cloudfront.Kind, cloudflare.Kind} {
		env := AppEnv(provider.StackSpec{
			Kind:     provider.StackApp,
			Recorded: true,
			Ref:      provider.StackRef{Name: naming.AppStack("production", "web", token)},
			Edge:     fakeEdgeOf(kind),
			App:      &provider.AppSpec{App: "web"},
		}, variables.Bundle{}, bucket.Sessions{}, "")

		if env[edge.ReleaseTokenVar] != token.String() {
			t.Errorf("%s = %q behind %s, want %q", edge.ReleaseTokenVar, env[edge.ReleaseTokenVar], kind, token)
		}
	}
}

func TestAContainerAppCloudflareForwardsToTheLoadBalancerIsToldNoEdgeCachesItsResponses(t *testing.T) {
	t.Parallel()

	env := AppEnv(provider.StackSpec{
		Kind:     provider.StackApp,
		Recorded: true,
		Ref:      provider.StackRef{Name: naming.AppStack("production", "web", naming.NewReleaseToken("b1", "f1"))},
		Edge:     fakeEdgeOf(cloudflare.Kind),
		App:      &provider.AppSpec{App: "web", Compute: provider.ComputeContainer, Router: alb.Kind},
	}, variables.Bundle{}, bucket.Sessions{}, "")

	for _, name := range []string{edge.EdgeCacheVar, edge.ReleaseTokenVar, edge.CacheTagPurgeVar} {
		if value, set := env[name]; set {
			t.Errorf("%s = %q, want it unset: Cloudflare forwards these requests to the load balancer and runs none of the edge's code, so nothing marks or stores them", name, value)
		}
	}
}

func TestAnAppWithAnOcelRouteTableIsToldWhereItsFunctionsFindIt(t *testing.T) {
	t.Parallel()

	env := AppEnv(provider.StackSpec{
		Kind:     provider.StackApp,
		Recorded: true,
		Edge:     fakeEdgeOf(cloudfront.Kind),
		App:      &provider.AppSpec{App: "web", OcelRouteTable: &provider.RoutingSpec{RootFunction: "index"}},
	}, variables.Bundle{}, bucket.Sessions{}, "")

	if want := "/var/task/" + edge.OcelRouteTableFile; env[edge.OcelRouteTableVar] != want {
		t.Errorf("%s = %q, want %q: the route table is bundled at the root of each function", edge.OcelRouteTableVar, env[edge.OcelRouteTableVar], want)
	}
	plain := AppEnv(provider.StackSpec{Kind: provider.StackApp, App: &provider.AppSpec{App: "web"}}, variables.Bundle{}, bucket.Sessions{}, "")
	if _, told := plain[edge.OcelRouteTableVar]; told {
		t.Errorf("%s is set for an app with no route table", edge.OcelRouteTableVar)
	}
}

func TestADispatchHostIsMadeOfANextRouteTableAlone(t *testing.T) {
	t.Parallel()

	spec := provider.StackSpec{
		Kind:     provider.StackApp,
		Recorded: true,
		App:      &provider.AppSpec{App: "web", OcelRouteTable: &provider.RoutingSpec{RootFunction: "index"}},
	}
	cfg := routedConfig(t, cloudfront.Kind)
	if host := NewDispatchHost(spec.App, cfg.Slug, cfg.EdgeBucket, cfg.ImageOptimizerURL); host != nil {
		t.Errorf("newDispatchHost = %+v, want none: an ocel route table dispatches between no functions", host)
	}
}

func TestAppEnvNamesTheRouterItsAppPromotesThroughAndNotItsEdge(t *testing.T) {
	t.Parallel()

	routed := func(kind router.Kind) map[string]string {
		return AppEnv(provider.StackSpec{
			Kind:     provider.StackApp,
			Recorded: true,
			Edge:     fakeEdgeOf(cloudfront.Kind),
			App:      &provider.AppSpec{App: "web", Router: kind},
		}, variables.Bundle{}, bucket.Sessions{}, "")
	}

	if env := routed("relay"); env["OCEL_ROUTER_KIND"] != "relay" {
		t.Errorf("OCEL_ROUTER_KIND = %q behind CloudFront for an app promoting through relay, want relay: the dispatch host names the router on every answer, and liveness expects the router the app pairs with", env["OCEL_ROUTER_KIND"])
	}
	if env := routed(""); env["OCEL_ROUTER_KIND"] != "" {
		t.Errorf("OCEL_ROUTER_KIND = %q for an app handed no router, want it unset", env["OCEL_ROUTER_KIND"])
	}
	if env := routed("relay"); env["OCEL_EDGE_KIND"] != "" {
		t.Errorf("OCEL_EDGE_KIND = %q, want it unset: nothing the function runs reads the edge's kind", env["OCEL_EDGE_KIND"])
	}
}

func TestTheEnvBudgetChargesForSiblingURLsStillToResolve(t *testing.T) {
	t.Parallel()

	host := newDispatchHostFor(t, routedConfig(t, cloudfront.Kind))

	base := map[string]string{routerKindEnv: string(cloudfront.Kind)}
	planned := host.plannedRootFunctionEnv(base, manifestAppFunctions(routedFunctions()))
	if charged := len(planned[functionURLsEnv]); charged < len("/admin")+functionURLBudgetBytes {
		t.Errorf("%s charges %d bytes, want an upper bound on the URL Pulumi resolves later", functionURLsEnv, charged)
	}
	if _, charged := host.rootFunctionEnv(base)[functionURLsEnv]; charged {
		t.Errorf("%s is charged twice; the plan-time bound is the only estimate", functionURLsEnv)
	}
}

type invokeStatement struct {
	Effect    string
	Action    []string
	Resource  json.RawMessage
	Condition map[string]map[string]string
}

func readDispatchInvokeGrant(t *testing.T, rec *inputRecorder) []invokeStatement {
	t.Helper()
	name := namedRole(testStack(t, "prod", "web")).Part(dispatchPolicyName)
	raw, ok := rec.inputs(rolePolicyToken, name)["policy"]
	if !ok || !raw.IsString() {
		t.Fatalf("the entry role has no %s policy", name)
	}
	var doc struct{ Statement []invokeStatement }
	if err := json.Unmarshal([]byte(raw.StringValue()), &doc); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return doc.Statement
}

func TestTheEntryRoleMayInvokeItsSiblingsAndTheOptimizer(t *testing.T) {
	t.Parallel()

	host := newDispatchHostFor(t, routedConfig(t, cloudfront.Kind))

	stack := testStack(t, "prod", "web")
	rec := &inputRecorder{}
	program := func(pctx *pulumi.Context) error {
		role, _, err := NewRole(pctx, component.AppRoleCoordinate("shop", stack), ExecutionRole{Namespace: testNamespace, Boundary: componenttest.BoundaryARN, Dispatch: host})
		if err != nil {
			return err
		}
		return AppStack{
			Namespace: testNamespace,
			Project:   "shop",
			Stack:     stack,
			Functions: manifestAppFunctions(routedFunctions()),
			Args:      argsFor(routedFunctions()),
			Artifacts: map[string]Artifact{},
			Layers:    testRuntimeLayers(),
			Env:       map[string]string{routerKindEnv: string(cloudfront.Kind)},
			Dispatch:  host,
			RoleArn:   role.Arn,
			RoleName:  role.Name,
		}.Declare(pctx)
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}

	statements := readDispatchInvokeGrant(t, rec)
	sibling := mockAccountARN("lambda", "function:"+namedFunction(stack, "fn--web--admin").Pulumi)

	var reachesSibling, reachesOptimizer bool
	for _, statement := range statements {
		if want := functionurl.InvokerActions(); !slices.Equal(statement.Action, want) {
			t.Errorf("statement grants %v, want %v: since October 2025 a signed URL call lacking either answers 403", statement.Action, want)
		}
		if strings.Contains(string(statement.Resource), `"*"`) {
			t.Errorf("statement reaches %s, want every resource named", statement.Resource)
		}
		if string(statement.Resource) == `["`+sibling+`"]` {
			reachesSibling = true
		}
		if statement.Condition["StringEquals"]["aws:ResourceTag/"+component.TagComponent] == tagImageOptimizer {
			if want := `"arn:aws:lambda:*:` + mockAccount + `:function:*"`; string(statement.Resource) != want {
				t.Errorf("optimizer statement reaches %s, want %s", statement.Resource, want)
			}
			reachesOptimizer = true
		}
	}
	if !reachesSibling {
		t.Errorf("statements %+v name no sibling ARN, so every sibling route answers 403", statements)
	}
	if !reachesOptimizer {
		t.Errorf("statements %+v reach no image optimizer, so /_next/image answers 502", statements)
	}
}

func TestAnAppBehindCloudflareGrantsNoInvoke(t *testing.T) {
	t.Parallel()

	stack := testStack(t, "prod", "web")
	rec := &inputRecorder{}
	program := func(pctx *pulumi.Context) error {
		return AppStack{
			Namespace: testNamespace,
			Project:   "shop",
			Stack:     stack,
			Functions: manifestAppFunctions(routedFunctions()),
			Args:      argsFor(routedFunctions()),
			Artifacts: map[string]Artifact{},
			Layers:    testRuntimeLayers(),
			Env:       map[string]string{},
			RoleArn:   pulumi.String("arn:aws:iam::123456789012:role/app"),
			RoleName:  pulumi.String("app"),
		}.Declare(pctx)
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}

	name := namedRole(testStack(t, "prod", "web")).Part(dispatchPolicyName)
	if _, granted := rec.inputs(rolePolicyToken, name)["policy"]; granted {
		t.Error("an app whose edge dispatches has an invoke grant, want the grant only where the origin dispatches")
	}
}

func TestDispatchHostIsToldTheStaticRulesTheBuildStates(t *testing.T) {
	t.Parallel()

	cfg := routedConfig(t, cloudfront.Kind)
	spec := routedSpec(t, cfg)
	spec.App.Static = &edge.Static{ImmutablePrefixes: []string{"/docs/_next/static/"}}
	host := NewDispatchHost(spec.App, cfg.Slug, cfg.EdgeBucket, cfg.ImageOptimizerURL)
	if got, want := host.Env[edge.StaticRulesVar], `{"immutablePrefixes":["/docs/_next/static/"]}`; got != want {
		t.Errorf("entry env %s = %q, want %q", edge.StaticRulesVar, got, want)
	}
}
