package deploy

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
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

const routedManifest = `{"entry":"/","buildId":"WEB1","basePath":"","pathnames":[],"routes":{},"dispatch":{}}`

func routedArtifactRoot(t *testing.T) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"apps/web/routing-manifest.json": routedManifest,
		"apps/web/serve.json":            `{"framework":"next","frameworkBuildId":"WEB1","edgeRouting":true,"entry":"/"}`,
	})
}

func routedConfig(t *testing.T, kind edge.Kind) Config {
	t.Helper()
	return Config{
		ArtifactRoot:      routedArtifactRoot(t),
		AssetBucket:       "assets-bucket",
		ImageOptimizerURL: "https://optimizer.lambda-url.us-east-1.on.aws/",
		RuntimeLayers:     testRuntimeLayers(),
		Slug:              "shop",
		Edge:              fakeEdgeOf(kind),
		AppBoundaryARN:    testBoundaryARN,
	}
}

func routedCoordinate(t *testing.T) naming.Coordinate {
	t.Helper()
	return storageCoordinate("prod", "shop", "web", fixedRelease(t))
}

func routedApp() *contractv1.ManifestApp {
	return &contractv1.ManifestApp{Name: "web", Framework: &contractv1.Framework{Name: buildoutput.FrameworkNext}}
}

func servingSpec(t *testing.T, cfg Config, app, runtime string, coord naming.Coordinate) provider.StackSpec {
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
		Ref:  provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: stack},
		Kind: provider.StackApp,
		Edge: cfg.Edge,
		App: &provider.AppSpec{
			App:         app,
			Framework:   runtime,
			BuildID:     "d1",
			Routing:     serving.OriginDispatch,
			Guard:       serving.Guard,
			AssetPrefix: serving.AssetPrefix,
		},
	}
}

func routedSpec(t *testing.T, cfg Config) provider.StackSpec {
	t.Helper()
	return servingSpec(t, cfg, "web", buildoutput.FrameworkNext, routedCoordinate(t))
}

func newDispatchHostFor(t *testing.T, cfg Config) *dispatchHost {
	t.Helper()
	host, err := releasing(t, cfg).newDispatchHost(routedSpec(t, cfg))
	if err != nil {
		t.Fatalf("newDispatchHost: %v", err)
	}
	return host
}

func TestDispatchHostNamesTheEntryAndWhatDispatchReads(t *testing.T) {
	t.Parallel()

	cfg := routedConfig(t, cloudfront.Kind)
	coord := routedCoordinate(t)
	host := newDispatchHostFor(t, cfg)
	if host == nil {
		t.Fatal("dispatch host = none, want the entry function to host dispatch")
	}
	if host.Entry != "/" {
		t.Errorf("entry = %q, want the route id the spec names", host.Entry)
	}
	want := map[string]string{
		routingManifestEnv:        routingManifestInTask,
		assetBucketEnv:            "assets-bucket",
		assetPrefixEnv:            appAssetPrefix(coord),
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
		role, err := newFunctionRole(pctx, roleCoordinate("shop", stack), executionRole{App: "web", Boundary: testBoundaryARN, Dispatch: host})
		if err != nil {
			return err
		}
		return appStackFunctions{
			Project:   "shop",
			Stack:     stack,
			Functions: manifestAppFunctions(routedFunctions()),
			Args:      argsFor(routedFunctions()),
			Artifacts: map[string]artifactRef{
				"fn--web--entry": {Bucket: "artifacts", Key: "entry.zip"},
				"fn--web--admin": {Bucket: "artifacts", Key: "admin.zip"},
			},
			Layers:   testRuntimeLayers(),
			Env:      map[string]string{routerKindEnv: string(cloudfront.Kind)},
			Dispatch: host,
			RoleArn:  role.Arn,
			RoleName: role.Name,
		}.register(pctx)
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}

	entry := functionEnvOf(t, rec, functionCoordinate("shop", stack, "fn--web--entry").PhysicalName(maxLambdaBaseNameLen))
	if entry[routerKindEnv] != string(cloudfront.Kind) {
		t.Errorf("%s = %q, want the router kind the deploy chose", routerKindEnv, entry[routerKindEnv])
	}
	if entry[routingManifestEnv] != routingManifestInTask {
		t.Errorf("%s = %q, want the manifest packed beside the handler", routingManifestEnv, entry[routingManifestEnv])
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
		return appStackFunctions{
			Project:   "shop",
			Stack:     stack,
			Functions: manifestAppFunctions(routedFunctions()),
			Args:      argsFor(routedFunctions()),
			Artifacts: map[string]artifactRef{},
			Layers:    testRuntimeLayers(),
			Env:       map[string]string{routerKindEnv: string(cloudfront.Kind)},
			Dispatch:  host,
			RoleArn:   pulumi.String("arn:aws:iam::123456789012:role/app"),
			RoleName:  pulumi.String("app"),
		}.register(pctx)
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}

	sibling := functionEnvOf(t, rec, functionCoordinate("shop", stack, "fn--web--admin").PhysicalName(maxLambdaBaseNameLen))
	if sibling[routerKindEnv] != string(cloudfront.Kind) {
		t.Errorf("%s = %q, want every function to learn the router kind", routerKindEnv, sibling[routerKindEnv])
	}
	for _, key := range []string{routingManifestEnv, functionURLsEnv} {
		if _, wired := sibling[key]; wired {
			t.Errorf("sibling has %s, want dispatch wired into the entry function alone", key)
		}
	}
}

func TestAppEnvPassesTheDeploymentURLToTheFunction(t *testing.T) {
	t.Parallel()

	app := routedApp()
	app.Variables = []*contractv1.ManifestVariable{
		{Key: processenv.AppURLEnvVar, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "https://shop.example"},
		{Key: processenv.ClientURLEnvVar, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "https://shop.example"},
	}

	env := plannedEnv(t, Config{}, app, nil)
	for _, key := range []string{processenv.AppURLEnvVar, processenv.ClientURLEnvVar} {
		if got, want := env[key], "https://shop.example"; got != want {
			t.Errorf("%s = %q, want %q: server code reads the url off its own environment", key, got, want)
		}
	}
}

func TestAnEdgeThatRunsNoCodeLeavesPathDispatchToTheOrigin(t *testing.T) {
	t.Parallel()

	behindCloudFront := plannedEnv(t, routedConfig(t, cloudfront.Kind), routedApp(), fakeEdgeOf(cloudfront.Kind))
	if behindCloudFront["OCEL_ORIGIN_DISPATCH"] != "1" {
		t.Errorf("OCEL_ORIGIN_DISPATCH = %q behind CloudFront, want \"1\": the entry function dispatches each path", behindCloudFront["OCEL_ORIGIN_DISPATCH"])
	}

	behindCloudflare := plannedEnv(t, routedConfig(t, cloudflare.Kind), routedApp(), fakeEdgeOf(cloudflare.Kind))
	if _, dispatches := behindCloudflare["OCEL_ORIGIN_DISPATCH"]; dispatches {
		t.Error("OCEL_ORIGIN_DISPATCH is set behind Cloudflare, want it unset: the edge dispatches each path")
	}
}

func TestAppEnvNamesTheRouterItsAppPromotesThroughAndNotItsEdge(t *testing.T) {
	t.Parallel()

	release := releasing(t, routedConfig(t, cloudfront.Kind))
	routed := func(kind router.Kind) map[string]string {
		return release.appEnv(provider.StackSpec{
			Kind: provider.StackApp,
			Edge: fakeEdgeOf(cloudfront.Kind),
			App:  &provider.AppSpec{App: "web", Router: kind},
		}, appBundle{}, sessionScope{})
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
	planned := host.plannedEntryEnv(base, manifestAppFunctions(routedFunctions()))
	if charged := len(planned[functionURLsEnv]); charged < len("/admin")+functionURLBudgetBytes {
		t.Errorf("%s charges %d bytes, want an upper bound on the URL Pulumi resolves later", functionURLsEnv, charged)
	}
	if _, charged := host.entryEnv(base)[functionURLsEnv]; charged {
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
	name := naming.ResourceID(naming.KindRole, roleLocalName, "policy", "dispatch", "invoke")
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
		role, err := newFunctionRole(pctx, roleCoordinate("shop", stack), executionRole{App: "web", Boundary: testBoundaryARN, Dispatch: host})
		if err != nil {
			return err
		}
		return appStackFunctions{
			Project:   "shop",
			Stack:     stack,
			Functions: manifestAppFunctions(routedFunctions()),
			Args:      argsFor(routedFunctions()),
			Artifacts: map[string]artifactRef{},
			Layers:    testRuntimeLayers(),
			Env:       map[string]string{routerKindEnv: string(cloudfront.Kind)},
			Dispatch:  host,
			RoleArn:   role.Arn,
			RoleName:  role.Name,
		}.register(pctx)
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}

	statements := readDispatchInvokeGrant(t, rec)
	sibling := mockAccountARN("lambda", "function:"+functionCoordinate("shop", stack, "fn--web--admin").PhysicalName(maxLambdaBaseNameLen))

	var reachesSibling, reachesOptimizer bool
	for _, statement := range statements {
		if !slices.Contains(statement.Action, "lambda:InvokeFunctionUrl") || len(statement.Action) != 1 {
			t.Errorf("statement grants %v, want the Function URL invoke alone", statement.Action)
		}
		if strings.Contains(string(statement.Resource), `"*"`) {
			t.Errorf("statement reaches %s, want every resource named", statement.Resource)
		}
		if string(statement.Resource) == `["`+sibling+`"]` {
			reachesSibling = true
		}
		if statement.Condition["StringEquals"]["aws:ResourceTag/"+tagComponent] == tagImageOptimizer {
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
		return appStackFunctions{
			Project:   "shop",
			Stack:     stack,
			Functions: manifestAppFunctions(routedFunctions()),
			Args:      argsFor(routedFunctions()),
			Artifacts: map[string]artifactRef{},
			Layers:    testRuntimeLayers(),
			Env:       map[string]string{},
			RoleArn:   pulumi.String("arn:aws:iam::123456789012:role/app"),
			RoleName:  pulumi.String("app"),
		}.register(pctx)
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}

	name := naming.ResourceID(naming.KindRole, roleLocalName, "policy", "dispatch", "invoke")
	if _, granted := rec.inputs(rolePolicyToken, name)["policy"]; granted {
		t.Error("an app whose edge dispatches has an invoke grant, want the grant only where the origin dispatches")
	}
}
