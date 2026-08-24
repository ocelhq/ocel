package deploy

import (
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const testOriginSecret = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"

const functionURLToken = "aws:lambda/functionUrl:FunctionUrl"

const lambdaPermissionToken = "aws:lambda/permission:Permission"

func guardedConfig(t *testing.T, kind edge.Kind) Config {
	t.Helper()
	cfg := routedConfig(t, kind)
	cfg.OriginSecret = testOriginSecret
	return cfg
}

func functionURLAuthOf(t *testing.T, rec *inputRecorder, logicalName string, stack naming.StackName) string {
	t.Helper()
	name := naming.ResourceID(naming.KindFunction, functionCoordinate("shop", stack, logicalName).Name, "url")
	value, ok := rec.inputs(functionURLToken, name)["authorizationType"]
	if !ok || !value.IsString() {
		t.Fatalf("%s carries no Function URL", logicalName)
	}
	return value.StringValue()
}

func registerGuarded(t *testing.T, cfg Config, functions []*contractv1.ManifestFunction, app *contractv1.ManifestApp, stack naming.StackName) *inputRecorder {
	t.Helper()
	host, err := resolveRouterHost(cfg, app, routedCoordinate(t), "d1")
	if err != nil {
		t.Fatalf("resolveRouterHost: %v", err)
	}
	guard, err := resolveOriginGuard(cfg, app)
	if err != nil {
		t.Fatalf("resolveOriginGuard: %v", err)
	}

	rec := &inputRecorder{}
	program := func(pctx *pulumi.Context) error {
		role, err := newFunctionRole(pctx, roleCoordinate("shop", stack), executionRole{App: app.GetName(), Router: host})
		if err != nil {
			return err
		}
		return appStackFunctions{
			Project:   "shop",
			Stack:     stack,
			Functions: manifestAppFunctions(functions),
			Args:      argsFor(functions),
			Artifacts: map[string]artifactRef{},
			Env:       appEnv(&contractv1.Manifest{}, app, appBundle{}, cfg, sessionScope{}),
			Router:    host,
			Guard:     guard,
			RoleArn:   role.Arn,
			RoleName:  role.Name,
		}.register(pctx)
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}
	return rec
}

func TestNoOriginSecretIsMintedForAnAppBehindCloudflare(t *testing.T) {
	t.Parallel()

	guard, err := resolveOriginGuard(guardedConfig(t, cloudflare.Kind), routedApp())
	if err != nil {
		t.Fatalf("resolveOriginGuard: %v", err)
	}
	if guard != nil {
		t.Errorf("guard = %+v, want none where the Cloudflare worker holds the front door", guard)
	}
}

func TestAnEntryFunctionTheInternetReachesRefusesToDeployWithoutASecret(t *testing.T) {
	t.Parallel()

	cfg := routedConfig(t, cloudfront.Kind)
	_, err := resolveOriginGuard(cfg, routedApp())
	if err == nil {
		t.Fatal("a public Function URL with no secret behind it was accepted, want the deploy refused")
	}
	if !strings.Contains(err.Error(), "ocel bootstrap") {
		t.Errorf("err = %v, want the message naming what mints the secret", err)
	}
}

func TestTheEntryFunctionAnswersWithoutSigV4AndDemandsTheSecret(t *testing.T) {
	t.Parallel()

	stack := testStack(t, "prod", "web")
	rec := registerGuarded(t, guardedConfig(t, cloudfront.Kind), routedFunctions(), routedApp(), stack)

	if auth := functionURLAuthOf(t, rec, "fn--web--entry", stack); auth != functionURLAuthNone {
		t.Errorf("entry Function URL auth = %q, want %q so a browser POST needs no signature", auth, functionURLAuthNone)
	}
	name := naming.ResourceID(naming.KindFunction, functionCoordinate("shop", stack, "fn--web--entry").Name, "url", "invoke")
	permission := rec.inputs(lambdaPermissionToken, name)
	if len(permission) == 0 {
		t.Fatal("the unsigned entry URL carries no invoke permission, so every request answers 403")
	}
	if got := permission["functionUrlAuthType"]; !got.IsString() || got.StringValue() != functionURLAuthNone {
		t.Errorf("permission functionUrlAuthType = %v, want it scoped to the unsigned URL alone", got)
	}

	entry := functionEnvOf(t, rec, functionCoordinate("shop", stack, "fn--web--entry").PhysicalName(maxLambdaBaseNameLen))
	if entry[edge.OriginSecretVar] != testOriginSecret {
		t.Errorf("%s = %q, want the secret the bootstrap holds", edge.OriginSecretVar, entry[edge.OriginSecretVar])
	}
	if _, signed := entry[edge.OriginSignedVar]; signed {
		t.Errorf("the entry carries %s, which waves its membrane past a URL nothing signs", edge.OriginSignedVar)
	}
}

func TestASiblingKeepsItsSignedURLAndLearnsNoSecret(t *testing.T) {
	t.Parallel()

	stack := testStack(t, "prod", "web")
	rec := registerGuarded(t, guardedConfig(t, cloudfront.Kind), routedFunctions(), routedApp(), stack)

	if auth := functionURLAuthOf(t, rec, "fn--web--admin", stack); auth != functionURLAuthIAM {
		t.Errorf("sibling Function URL auth = %q, want %q; only the entry answers the internet", auth, functionURLAuthIAM)
	}
	name := naming.ResourceID(naming.KindFunction, functionCoordinate("shop", stack, "fn--web--admin").Name, "url", "invoke")
	if len(rec.inputs(lambdaPermissionToken, name)) != 0 {
		t.Error("a sibling carries an unsigned invoke permission, want the front door opened once")
	}
	sibling := functionEnvOf(t, rec, functionCoordinate("shop", stack, "fn--web--admin").PhysicalName(maxLambdaBaseNameLen))
	if _, wired := sibling[edge.OriginSecretVar]; wired {
		t.Errorf("a sibling carries %s, want the secret to reach the entry function alone", edge.OriginSecretVar)
	}
	if sibling[edge.OriginSignedVar] == "" {
		t.Errorf("a sibling carries no %s, so its membrane refuses the entry's signed requests", edge.OriginSignedVar)
	}
}

func TestEveryFunctionURLBehindCloudflareStaysSigned(t *testing.T) {
	t.Parallel()

	stack := testStack(t, "prod", "web")
	rec := registerGuarded(t, guardedConfig(t, cloudflare.Kind), routedFunctions(), routedApp(), stack)

	for _, logical := range []string{"fn--web--entry", "fn--web--admin"} {
		if auth := functionURLAuthOf(t, rec, logical, stack); auth != functionURLAuthIAM {
			t.Errorf("%s Function URL auth = %q, want %q behind the Cloudflare worker", logical, auth, functionURLAuthIAM)
		}
		name := naming.ResourceID(naming.KindFunction, functionCoordinate("shop", stack, logical).Name, "url", "invoke")
		if len(rec.inputs(lambdaPermissionToken, name)) != 0 {
			t.Errorf("%s carries a public invoke permission behind the Cloudflare worker", logical)
		}
		env := functionEnvOf(t, rec, functionCoordinate("shop", stack, logical).PhysicalName(maxLambdaBaseNameLen))
		if _, wired := env[edge.OriginSecretVar]; wired {
			t.Errorf("%s carries %s behind an edge that signs its requests", logical, edge.OriginSecretVar)
		}
	}
}

func TestNoneModeReachesItsEntryOverASignedURL(t *testing.T) {
	t.Parallel()

	cfg := guardedConfig(t, apigateway.Kind)
	guard, err := resolveOriginGuard(cfg, routedApp())
	if err != nil {
		t.Fatalf("resolveOriginGuard: %v", err)
	}
	if guard != nil {
		t.Fatalf("guard = %+v, want none: none mode reaches the entry through API Gateway's execution role", guard)
	}

	stack := testStack(t, "prod", "web")
	rec := registerGuarded(t, cfg, routedFunctions(), routedApp(), stack)
	for _, logical := range []string{"fn--web--entry", "fn--web--admin"} {
		if auth := functionURLAuthOf(t, rec, logical, stack); auth != functionURLAuthIAM {
			t.Errorf("%s Function URL auth = %q, want %q; none mode adds no resource policy to a release function", logical, auth, functionURLAuthIAM)
		}
		name := naming.ResourceID(naming.KindFunction, functionCoordinate("shop", stack, logical).Name, "url", "invoke")
		if len(rec.inputs(lambdaPermissionToken, name)) != 0 {
			t.Errorf("%s carries a public invoke permission; none mode signs every integration", logical)
		}
		env := functionEnvOf(t, rec, functionCoordinate("shop", stack, logical).PhysicalName(maxLambdaBaseNameLen))
		if _, wired := env[edge.OriginSecretVar]; wired {
			t.Errorf("%s carries %s, which an AWS_PROXY integration never presents", logical, edge.OriginSecretVar)
		}
		if env[edge.OriginSignedVar] == "" {
			t.Errorf("%s carries no %s, so its membrane refuses every signed request", logical, edge.OriginSignedVar)
		}
	}
}

func TestAnAppThatRoutesNothingStillGuardsItsEntry(t *testing.T) {
	t.Parallel()

	cfg := guardedConfig(t, cloudfront.Kind)
	cfg.ArtifactRoot = writeTree(t, map[string]string{
		"apps/api/serve.json": `{"framework":"express","buildId":"API1","entry":"/"}`,
	})
	app := &contractv1.ManifestApp{Name: "api", Framework: "express"}
	functions := []*contractv1.ManifestFunction{
		{LogicalName: "fn--api--entry", App: "api", RouteId: "/"},
	}

	stack := testStack(t, "prod", "api")
	guard, err := resolveOriginGuard(cfg, app)
	if err != nil {
		t.Fatalf("resolveOriginGuard: %v", err)
	}
	rec := &inputRecorder{}
	program := func(pctx *pulumi.Context) error {
		return appStackFunctions{
			Project:   "shop",
			Stack:     stack,
			Functions: manifestAppFunctions(functions),
			Args:      argsFor(functions),
			Artifacts: map[string]artifactRef{},
			Env:       appEnv(&contractv1.Manifest{}, app, appBundle{}, cfg, sessionScope{}),
			Guard:     guard,
			RoleArn:   pulumi.String("arn:aws:iam::123456789012:role/app"),
			RoleName:  pulumi.String("app"),
		}.register(pctx)
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--api", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}

	name := naming.ResourceID(naming.KindFunction, functionCoordinate("shop", stack, "fn--api--entry").Name, "url")
	value := rec.inputs(functionURLToken, name)["authorizationType"]
	if !value.IsString() || value.StringValue() != functionURLAuthNone {
		t.Errorf("entry Function URL auth = %v, want %q for the sole function of an app no router fronts", value, functionURLAuthNone)
	}
	env := functionEnvOf(t, rec, functionCoordinate("shop", stack, "fn--api--entry").PhysicalName(maxLambdaBaseNameLen))
	if env[edge.OriginSecretVar] != testOriginSecret {
		t.Errorf("%s = %q, want the secret on an entry nothing else guards", edge.OriginSecretVar, env[edge.OriginSecretVar])
	}
	if _, routed := env[functionURLsEnv]; routed {
		t.Errorf("an app that routes nothing carries %s", functionURLsEnv)
	}
}
