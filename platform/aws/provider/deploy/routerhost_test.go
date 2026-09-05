package deploy

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const routedManifest = `{"entry":"/","buildId":"WEB1","basePath":"","pathnames":[],"routes":{},"dispatch":{}}`

func routedArtifactRoot(t *testing.T) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"apps/web/routing-manifest.json": routedManifest,
		"apps/web/serve.json":            `{"runtime":"next","buildId":"WEB1","edgeRouting":true,"entry":"/"}`,
	})
}

func routedConfig(t *testing.T, kind edge.Kind) Config {
	t.Helper()
	return Config{
		ArtifactRoot:      routedArtifactRoot(t),
		AssetBucket:       "assets-bucket",
		ImageOptimizerURL: "https://optimizer.lambda-url.us-east-1.on.aws/",
		Slug:              "shop",
		Edge:              fakeEdgeOf(kind),
	}
}

func routedCoordinate(t *testing.T) naming.Coordinate {
	t.Helper()
	return storageCoordinate("prod", "shop", "web", fixedRelease(t))
}

func routedApp() *contractv1.ManifestApp {
	return &contractv1.ManifestApp{Name: "web", Runtime: &contractv1.Runtime{Name: runtimeNext}}
}

func servingPlan(t *testing.T, cfg Config, app, runtime string, coord naming.Coordinate) providerkit.StackPlan {
	t.Helper()
	stack := coord.Stack()
	facts := cfg.Edge.Facts()
	serving, err := providerkit.ServingFactsFor(providerkit.ServingQuery{
		Root:              cfg.ArtifactRoot,
		Project:           "shop",
		App:               app,
		Runtime:           runtime,
		Stack:             stack,
		Coordinate:        coord,
		EdgeRunsCode:      facts.RunsCode,
		EdgeSignsForwards: facts.SignsOriginForwards,
	})
	if err != nil {
		t.Fatalf("ServingFactsFor: %v", err)
	}
	return providerkit.StackPlan{
		Ref:  providerkit.StackRef{Project: "shop", Class: providerkit.ClassProduction, Name: stack},
		Kind: providerkit.StackApp,
		Edge: cfg.Edge,
		App: &providerkit.AppPlan{
			App:         app,
			Runtime:     runtime,
			Deployment:  "d1",
			Routing:     serving.Routing,
			Guard:       serving.Guard,
			AssetPrefix: serving.AssetPrefix,
		},
	}
}

func routedPlan(t *testing.T, cfg Config) providerkit.StackPlan {
	t.Helper()
	return servingPlan(t, cfg, "web", runtimeNext, routedCoordinate(t))
}

func routedRouter(t *testing.T, cfg Config) *routerHost {
	t.Helper()
	host, err := releasing(t, cfg).routerHost(routedPlan(t, cfg))
	if err != nil {
		t.Fatalf("routerHost: %v", err)
	}
	return host
}

func TestRouterHostNamesTheEntryAndWhatTheRouterReads(t *testing.T) {
	t.Parallel()

	cfg := routedConfig(t, cloudfront.Kind)
	coord := routedCoordinate(t)
	host := routedRouter(t, cfg)
	if host == nil {
		t.Fatal("router host = none, want the entry function to host the router")
	}
	if host.Entry != "/" {
		t.Errorf("entry = %q, want the route id the plan names", host.Entry)
	}
	want := map[string]string{
		routingManifestEnv:        routingManifestInTask,
		assetBucketEnv:            "assets-bucket",
		assetPrefixEnv:            appAssetPrefix(coord),
		slugEnv:                   "shop",
		appNameEnv:                "web",
		deploymentIDEnv:           "d1",
		edge.ImageOptimizerURLVar: "https://optimizer.lambda-url.us-east-1.on.aws/",
		edge.OriginBodyLimitVar:   strconv.Itoa(lambdaOriginBodyLimitBytes),
	}
	for key, value := range want {
		if host.Env[key] != value {
			t.Errorf("entry env %s = %q, want %q", key, host.Env[key], value)
		}
	}
}

func routedFunctions() []*contractv1.ManifestFunction {
	return []*contractv1.ManifestFunction{
		{LogicalName: "fn--web--entry", App: "web", Runtime: &contractv1.Runtime{Name: runtimeNext}, RouteId: "/"},
		{LogicalName: "fn--web--admin", App: "web", Runtime: &contractv1.Runtime{Name: runtimeNext}, RouteId: "/admin"},
	}
}

func functionEnvOf(t *testing.T, rec *inputRecorder, name string) map[string]string {
	t.Helper()
	variables := rec.object(t, "aws:lambda/function:Function", name, "environment")["variables"]
	if !variables.IsObject() {
		t.Fatalf("function %s carries no environment variables", name)
	}
	env := map[string]string{}
	for key, value := range variables.ObjectValue() {
		if value.IsString() {
			env[string(key)] = value.StringValue()
		}
	}
	return env
}

func TestEntryFunctionCarriesTheEdgeKindAndItsSiblingURLs(t *testing.T) {
	t.Parallel()

	cfg := routedConfig(t, cloudfront.Kind)
	host := routedRouter(t, cfg)

	stack := testStack(t, "prod", "web")
	rec := &inputRecorder{}
	program := func(pctx *pulumi.Context) error {
		role, err := newFunctionRole(pctx, roleCoordinate("shop", stack), executionRole{App: "web", Router: host})
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
			Env:      map[string]string{edgeKindEnv: string(cloudfront.Kind)},
			Router:   host,
			RoleArn:  role.Arn,
			RoleName: role.Name,
		}.register(pctx)
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}

	entry := functionEnvOf(t, rec, functionCoordinate("shop", stack, "fn--web--entry").PhysicalName(maxLambdaBaseNameLen))
	if entry[edgeKindEnv] != string(cloudfront.Kind) {
		t.Errorf("%s = %q, want the edge kind the deploy chose", edgeKindEnv, entry[edgeKindEnv])
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

func TestASiblingFunctionHostsNoRouter(t *testing.T) {
	t.Parallel()

	cfg := routedConfig(t, cloudfront.Kind)
	host := routedRouter(t, cfg)

	stack := testStack(t, "prod", "web")
	rec := &inputRecorder{}
	program := func(pctx *pulumi.Context) error {
		return appStackFunctions{
			Project:   "shop",
			Stack:     stack,
			Functions: manifestAppFunctions(routedFunctions()),
			Args:      argsFor(routedFunctions()),
			Artifacts: map[string]artifactRef{},
			Env:       map[string]string{edgeKindEnv: string(cloudfront.Kind)},
			Router:    host,
			RoleArn:   pulumi.String("arn:aws:iam::123456789012:role/app"),
			RoleName:  pulumi.String("app"),
		}.register(pctx)
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}

	sibling := functionEnvOf(t, rec, functionCoordinate("shop", stack, "fn--web--admin").PhysicalName(maxLambdaBaseNameLen))
	if sibling[edgeKindEnv] != string(cloudfront.Kind) {
		t.Errorf("%s = %q, want every function to learn the edge kind", edgeKindEnv, sibling[edgeKindEnv])
	}
	for _, key := range []string{routingManifestEnv, functionURLsEnv} {
		if _, wired := sibling[key]; wired {
			t.Errorf("sibling carries %s, want the router wired into the entry function alone", key)
		}
	}
}

func TestAppEnvCarriesTheDeploymentURLToTheFunction(t *testing.T) {
	t.Parallel()

	app := routedApp()
	app.Variables = []*contractv1.ManifestVariable{
		{Key: providerkit.URLEnvName, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "https://shop.example"},
		{Key: providerkit.ClientURLEnvName, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "https://shop.example"},
	}

	env := plannedEnv(t, Config{}, app, nil)
	for _, key := range []string{providerkit.URLEnvName, providerkit.ClientURLEnvName} {
		if got, want := env[key], "https://shop.example"; got != want {
			t.Errorf("%s = %q, want %q: server code reads the url off its own environment", key, got, want)
		}
	}
}

func TestAppEnvNamesTheEdgeKind(t *testing.T) {
	t.Parallel()

	env := plannedEnv(t, routedConfig(t, cloudfront.Kind), routedApp(), fakeEdgeOf(cloudfront.Kind))
	if env[edgeKindEnv] != string(cloudfront.Kind) {
		t.Errorf("%s = %q, want the kind of edge the deploy chose", edgeKindEnv, env[edgeKindEnv])
	}

	behind := plannedEnv(t, routedConfig(t, cloudflare.Kind), routedApp(), fakeEdgeOf(cloudflare.Kind))
	if behind[edgeKindEnv] != string(cloudflare.Kind) {
		t.Errorf("%s = %q, want the Cloudflare kind named too", edgeKindEnv, behind[edgeKindEnv])
	}

	none := plannedEnv(t, Config{}, routedApp(), nil)
	if _, named := none[edgeKindEnv]; named {
		t.Errorf("%s = %q, want no kind where the deploy binds no edge", edgeKindEnv, none[edgeKindEnv])
	}
}

func TestTheEnvBudgetChargesForSiblingURLsStillToResolve(t *testing.T) {
	t.Parallel()

	host := routedRouter(t, routedConfig(t, cloudfront.Kind))

	base := map[string]string{edgeKindEnv: string(cloudfront.Kind)}
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

func routerInvokeGrant(t *testing.T, rec *inputRecorder) []invokeStatement {
	t.Helper()
	name := naming.ResourceID(naming.KindRole, roleLocalName, "policy", "router", "invoke")
	raw, ok := rec.inputs(rolePolicyToken, name)["policy"]
	if !ok || !raw.IsString() {
		t.Fatalf("the entry role carries no %s policy", name)
	}
	var doc struct{ Statement []invokeStatement }
	if err := json.Unmarshal([]byte(raw.StringValue()), &doc); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return doc.Statement
}

func TestTheEntryRoleMayInvokeItsSiblingsAndTheOptimizer(t *testing.T) {
	t.Parallel()

	host := routedRouter(t, routedConfig(t, cloudfront.Kind))

	stack := testStack(t, "prod", "web")
	rec := &inputRecorder{}
	program := func(pctx *pulumi.Context) error {
		role, err := newFunctionRole(pctx, roleCoordinate("shop", stack), executionRole{App: "web", Router: host})
		if err != nil {
			return err
		}
		return appStackFunctions{
			Project:   "shop",
			Stack:     stack,
			Functions: manifestAppFunctions(routedFunctions()),
			Args:      argsFor(routedFunctions()),
			Artifacts: map[string]artifactRef{},
			Env:       map[string]string{edgeKindEnv: string(cloudfront.Kind)},
			Router:    host,
			RoleArn:   role.Arn,
			RoleName:  role.Name,
		}.register(pctx)
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}

	statements := routerInvokeGrant(t, rec)
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
			Env:       map[string]string{},
			RoleArn:   pulumi.String("arn:aws:iam::123456789012:role/app"),
			RoleName:  pulumi.String("app"),
		}.register(pctx)
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}

	name := naming.ResourceID(naming.KindRole, roleLocalName, "policy", "router", "invoke")
	if _, granted := rec.inputs(rolePolicyToken, name)["policy"]; granted {
		t.Error("an app whose edge routes carries an invoke grant, want the grant only where the origin routes")
	}
}
