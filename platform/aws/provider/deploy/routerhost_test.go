package deploy

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const routedManifest = `{"entry":"/","buildId":"WEB1","basePath":"","pathnames":[],"routes":{},"dispatch":{}}`

func routedArtifactRoot(t *testing.T) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"apps/web/routing-manifest.json": routedManifest,
		"apps/web/serve.json":            `{"framework":"next","buildId":"WEB1","edgeRouting":true,"entry":"/"}`,
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
	return &contractv1.ManifestApp{Name: "web", Framework: frameworkNext}
}

func TestRouterHostStaysUnbuiltBehindCloudflare(t *testing.T) {
	t.Parallel()

	host, err := resolveRouterHost(routedConfig(t, cloudflare.Kind), routedApp(), routedCoordinate(t), "d1")
	if err != nil {
		t.Fatalf("resolveRouterHost: %v", err)
	}
	if host != nil {
		t.Errorf("router host = %+v, want none where the Cloudflare worker routes", host)
	}
}

func TestRouterHostStaysUnbuiltForAnAppThatRoutesNothing(t *testing.T) {
	t.Parallel()

	cfg := routedConfig(t, cloudfront.Kind)
	cfg.ArtifactRoot = writeTree(t, map[string]string{
		"apps/api/serve.json": `{"framework":"express","buildId":"API1"}`,
	})

	host, err := resolveRouterHost(cfg, &contractv1.ManifestApp{Name: "api", Framework: "express"}, routedCoordinate(t), "d1")
	if err != nil {
		t.Fatalf("resolveRouterHost: %v", err)
	}
	if host != nil {
		t.Errorf("router host = %+v, want none for an app whose build declares no routing", host)
	}
}

func TestRouterHostNamesTheEntryAndWhatTheRouterReads(t *testing.T) {
	t.Parallel()

	coord := routedCoordinate(t)
	host, err := resolveRouterHost(routedConfig(t, cloudfront.Kind), routedApp(), coord, "d1")
	if err != nil {
		t.Fatalf("resolveRouterHost: %v", err)
	}
	if host == nil {
		t.Fatal("router host = none, want the entry function to host the router")
	}
	if host.Entry != "/" {
		t.Errorf("entry = %q, want the route id serve.json names", host.Entry)
	}
	if string(host.Manifest) != routedManifest {
		t.Errorf("manifest = %q, want the routing manifest the build wrote", host.Manifest)
	}
	want := map[string]string{
		routingManifestEnv:         routingManifestInTask,
		assetBucketEnv:             "assets-bucket",
		assetPrefixEnv:             appAssetPrefix(coord),
		slugEnv:                    "shop",
		appNameEnv:                 "web",
		deploymentIDEnv:            "d1",
		edge.ImageOptimizerURLVar:  "https://optimizer.lambda-url.us-east-1.on.aws/",
		edge.OriginBodyLimitVar:    "6289408",
		edge.OriginBodyEncodingVar: edge.OriginBodyEncodingBase64,
	}
	for key, value := range want {
		if host.Env[key] != value {
			t.Errorf("entry env %s = %q, want %q", key, host.Env[key], value)
		}
	}
}

func TestRouterHostRefusesARoutedBuildThatNamesNoEntry(t *testing.T) {
	t.Parallel()

	cfg := routedConfig(t, cloudfront.Kind)
	cfg.ArtifactRoot = writeTree(t, map[string]string{
		"apps/web/routing-manifest.json": routedManifest,
		"apps/web/serve.json":            `{"framework":"next","buildId":"WEB1","edgeRouting":true}`,
	})

	if _, err := resolveRouterHost(cfg, routedApp(), routedCoordinate(t), "d1"); err == nil {
		t.Error("a routed build naming no entry was accepted, want the deploy refused")
	}
}

func routedFunctions() []*contractv1.ManifestFunction {
	return []*contractv1.ManifestFunction{
		{LogicalName: "fn--web--entry", App: "web", Framework: frameworkNext, RouteId: "/"},
		{LogicalName: "fn--web--admin", App: "web", Framework: frameworkNext, RouteId: "/admin"},
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
	coord := routedCoordinate(t)
	host, err := resolveRouterHost(cfg, routedApp(), coord, "d1")
	if err != nil {
		t.Fatalf("resolveRouterHost: %v", err)
	}

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
	host, err := resolveRouterHost(cfg, routedApp(), routedCoordinate(t), "d1")
	if err != nil {
		t.Fatalf("resolveRouterHost: %v", err)
	}

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

func TestOnlyTheEntryFunctionPacksTheRoutingManifest(t *testing.T) {
	t.Parallel()

	cfg := routedConfig(t, cloudfront.Kind)
	host, err := resolveRouterHost(cfg, routedApp(), routedCoordinate(t), "d1")
	if err != nil {
		t.Fatalf("resolveRouterHost: %v", err)
	}
	functions := manifestAppFunctions(routedFunctions())
	dir := filepath.Join(cfg.ArtifactRoot, "apps", "web")

	packed, err := zipDir(dir, withOverlay(nil, host.overlay()))
	if err != nil {
		t.Fatalf("zipDir: %v", err)
	}
	if !host.hosts(functions[0]) || host.hosts(functions[1]) {
		t.Fatal("the entry predicate does not single out the entry function")
	}
	if got := zipEntry(t, packed, edge.RoutingManifestFile); got != routedManifest {
		t.Errorf("packed %s = %q, want the routing manifest the router reads", edge.RoutingManifestFile, got)
	}
}

func zipEntry(t *testing.T, archive []byte, name string) string {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	for _, file := range reader.File {
		if file.Name != name {
			continue
		}
		opened, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		defer opened.Close()
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(opened); err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return buf.String()
	}
	t.Fatalf("zip carries no %s", name)
	return ""
}

func TestRouterHostRefusesARoutedBuildThatWroteNoManifest(t *testing.T) {
	t.Parallel()

	cfg := routedConfig(t, cloudfront.Kind)
	cfg.ArtifactRoot = writeTree(t, map[string]string{
		"apps/web/serve.json": `{"framework":"next","buildId":"WEB1","edgeRouting":true,"entry":"/"}`,
	})

	if _, err := resolveRouterHost(cfg, routedApp(), routedCoordinate(t), "d1"); err == nil {
		t.Error("a routed build missing its routing manifest was accepted, want the deploy refused")
	}
}

func TestAppEnvNamesTheEdgeKind(t *testing.T) {
	t.Parallel()

	manifest := &contractv1.Manifest{Slug: "shop", Apps: []*contractv1.ManifestApp{routedApp()}}

	env := appEnv(manifest, routedApp(), appBundle{}, routedConfig(t, cloudfront.Kind), sessionScope{})
	if env[edgeKindEnv] != string(cloudfront.Kind) {
		t.Errorf("%s = %q, want the kind of edge the deploy chose", edgeKindEnv, env[edgeKindEnv])
	}

	behind := appEnv(manifest, routedApp(), appBundle{}, routedConfig(t, cloudflare.Kind), sessionScope{})
	if behind[edgeKindEnv] != string(cloudflare.Kind) {
		t.Errorf("%s = %q, want the Cloudflare kind named too", edgeKindEnv, behind[edgeKindEnv])
	}

	none := appEnv(manifest, routedApp(), appBundle{}, Config{}, sessionScope{})
	if _, named := none[edgeKindEnv]; named {
		t.Errorf("%s = %q, want no kind where the deploy binds no edge", edgeKindEnv, none[edgeKindEnv])
	}
}

func TestTheEnvBudgetChargesForSiblingURLsStillToResolve(t *testing.T) {
	t.Parallel()

	host, err := resolveRouterHost(routedConfig(t, cloudfront.Kind), routedApp(), routedCoordinate(t), "d1")
	if err != nil {
		t.Fatalf("resolveRouterHost: %v", err)
	}

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

	host, err := resolveRouterHost(routedConfig(t, cloudfront.Kind), routedApp(), routedCoordinate(t), "d1")
	if err != nil {
		t.Fatalf("resolveRouterHost: %v", err)
	}

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
