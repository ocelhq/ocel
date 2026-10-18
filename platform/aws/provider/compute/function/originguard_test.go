package function

import (
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/functionurl"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/bucket"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

const testOriginSecret = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"

const functionURLToken = "aws:lambda/functionUrl:FunctionUrl"

const lambdaPermissionToken = "aws:lambda/permission:Permission"

func publicPermissionID(stack naming.StackName, logical string, permission functionurl.Permission) string {
	return naming.ResourceID(naming.KindFunction, Coordinate("shop", stack, logical).Name, "url", strings.ToLower(permission.Verb()))
}

func hasPublicPermission(rec *inputRecorder, stack naming.StackName, logical string) bool {
	for _, permission := range functionurl.Public() {
		if len(rec.inputs(lambdaPermissionToken, publicPermissionID(stack, logical, permission))) != 0 {
			return true
		}
	}
	return false
}

func TestAnEntryDeployedDuringARotationIsHandedTheSecretItReplacedToo(t *testing.T) {
	t.Parallel()

	rotating := &OriginGuard{RootFunction: "/", Secret: testOriginSecret, Previous: "0ld"}
	env := rotating.rootFunctionEnv(map[string]string{edge.OriginSignedVar: "1"})
	if env[edge.OriginSecretVar] != testOriginSecret || env[edge.OriginSecretPreviousVar] != "0ld" {
		t.Errorf("entry env = %v, want both secrets: a route written before the rotation still presents the old one", env)
	}
	if _, signed := env[edge.OriginSignedVar]; signed {
		t.Errorf("entry env = %v, still waved past the guard", env)
	}
	if _, fronted := env[edge.OriginFrontedVar]; !fronted {
		t.Errorf("entry env = %v, not marked fronted", env)
	}

	unrotated := &OriginGuard{RootFunction: "/", Secret: testOriginSecret}
	if _, present := unrotated.rootFunctionEnv(nil)[edge.OriginSecretPreviousVar]; present {
		t.Error("a tier with no rotation underway hands the entry a predecessor")
	}
}

func guardedConfig(t *testing.T, kind edge.Kind) testConfig {
	t.Helper()
	cfg := routedConfig(t, kind)
	cfg.OriginSecret = testOriginSecret
	return cfg
}

func functionURLAuthOf(t *testing.T, rec *inputRecorder, logicalName string, stack naming.StackName) string {
	t.Helper()
	name := naming.ResourceID(naming.KindFunction, Coordinate("shop", stack, logicalName).Name, "url")
	value, ok := rec.inputs(functionURLToken, name)["authorizationType"]
	if !ok || !value.IsString() {
		t.Fatalf("%s has no Function URL", logicalName)
	}
	return value.StringValue()
}

func registerGuarded(t *testing.T, cfg testConfig, spec provider.StackSpec, functions []*contractv1.ManifestFunction, stack naming.StackName) *inputRecorder {
	t.Helper()
	host := NewDispatchHost(spec.App, cfg.Slug, cfg.AssetBucket, cfg.ImageOptimizerURL)
	guard, err := NewOriginGuard(spec.App, cfg.Tier, cfg.OriginSecret, cfg.PreviousOriginSecret)
	if err != nil {
		t.Fatalf("originGuard: %v", err)
	}

	rec := &inputRecorder{}
	program := func(pctx *pulumi.Context) error {
		role, err := NewRole(pctx, component.AppRoleCoordinate("shop", stack), ExecutionRole{App: spec.App.App, Boundary: componenttest.BoundaryARN, Dispatch: host})
		if err != nil {
			return err
		}
		return AppStack{
			Project:   "shop",
			Stack:     stack,
			Functions: manifestAppFunctions(functions),
			Args:      argsFor(functions),
			Artifacts: map[string]Artifact{},
			Layers:    testRuntimeLayers(),
			Env:       AppEnv(spec, variables.Bundle{}, bucket.Sessions{}, cfg.StateTable),
			Dispatch:  host,
			Guard:     guard,
			RoleArn:   role.Arn,
			RoleName:  role.Name,
		}.Declare(pctx)
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", stack.String(), rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}
	return rec
}

func registerRoutedGuarded(t *testing.T, kind edge.Kind, stack naming.StackName) *inputRecorder {
	t.Helper()
	cfg := guardedConfig(t, kind)
	return registerGuarded(t, cfg, routedSpec(t, cfg), routedFunctions(), stack)
}

func TestTheEntryFunctionAnswersWithoutSigV4AndDemandsTheSecret(t *testing.T) {
	t.Parallel()

	stack := testStack(t, "prod", "web")
	rec := registerRoutedGuarded(t, cloudfront.Kind, stack)

	if auth := functionURLAuthOf(t, rec, "fn--web--entry", stack); auth != functionurl.AuthNone {
		t.Errorf("entry Function URL auth = %q, want %q so a browser POST needs no signature", auth, functionurl.AuthNone)
	}
	for _, want := range functionurl.Public() {
		permission := rec.inputs(lambdaPermissionToken, publicPermissionID(stack, "fn--web--entry", want))
		if len(permission) == 0 {
			t.Fatalf("the unsigned entry URL has no %s permission, so every request answers 403", want.Action)
		}
		if got := permission["action"]; !got.IsString() || got.StringValue() != want.Action {
			t.Errorf("permission action = %v, want %s", got, want.Action)
		}
		if got := permission["principal"]; !got.IsString() || got.StringValue() != "*" {
			t.Errorf("%s principal = %v, want anyone, whom the origin secret then sorts", want.Action, got)
		}
		if want.URLAuthType != "" {
			if got := permission["functionUrlAuthType"]; !got.IsString() || got.StringValue() != want.URLAuthType {
				t.Errorf("%s functionUrlAuthType = %v, want it scoped to the unsigned URL alone", want.Action, got)
			}
		}
		if want.ViaURL {
			if got := permission["invokedViaFunctionUrl"]; !got.IsBool() || !got.BoolValue() {
				t.Errorf("%s invokedViaFunctionUrl = %v, want it held to calls through the URL", want.Action, got)
			}
		}
	}

	entry := functionEnvOf(t, rec, Coordinate("shop", stack, "fn--web--entry").PhysicalName(component.MaxLambdaBaseNameLen))
	if entry[edge.OriginSecretVar] != testOriginSecret {
		t.Errorf("%s = %q, want the secret the bootstrap stored", edge.OriginSecretVar, entry[edge.OriginSecretVar])
	}
	if _, fronted := entry[edge.OriginFrontedVar]; !fronted {
		t.Errorf("the entry has no %s, so an entry whose secret is lost on the way answers anyone on its public URL", edge.OriginFrontedVar)
	}
	if _, signed := entry[edge.OriginSignedVar]; signed {
		t.Errorf("the entry has %s, which waves its runtime past a URL nothing signs", edge.OriginSignedVar)
	}
}

func TestASiblingKeepsItsSignedURLAndLearnsNoSecret(t *testing.T) {
	t.Parallel()

	stack := testStack(t, "prod", "web")
	rec := registerRoutedGuarded(t, cloudfront.Kind, stack)

	if auth := functionURLAuthOf(t, rec, "fn--web--admin", stack); auth != functionurl.AuthIAM {
		t.Errorf("sibling Function URL auth = %q, want %q; only the entry answers the internet", auth, functionurl.AuthIAM)
	}
	for _, permission := range functionurl.Public() {
		if len(rec.inputs(lambdaPermissionToken, publicPermissionID(stack, "fn--web--admin", permission))) != 0 {
			t.Errorf("a sibling has a public %s permission, want the front door opened once", permission.Action)
		}
	}
	sibling := functionEnvOf(t, rec, Coordinate("shop", stack, "fn--web--admin").PhysicalName(component.MaxLambdaBaseNameLen))
	if _, wired := sibling[edge.OriginSecretVar]; wired {
		t.Errorf("a sibling has %s, want the secret to reach the root function alone", edge.OriginSecretVar)
	}
	if _, fronted := sibling[edge.OriginFrontedVar]; fronted {
		t.Errorf("a sibling has %s, and it is handed no secret: it would refuse to start", edge.OriginFrontedVar)
	}
	if sibling[edge.OriginSignedVar] == "" {
		t.Errorf("a sibling has no %s, so its runtime refuses the entry's signed requests", edge.OriginSignedVar)
	}
}

func TestEveryFunctionURLBehindCloudflareStaysSigned(t *testing.T) {
	t.Parallel()

	stack := testStack(t, "prod", "web")
	rec := registerRoutedGuarded(t, cloudflare.Kind, stack)

	for _, logical := range []string{"fn--web--entry", "fn--web--admin"} {
		if auth := functionURLAuthOf(t, rec, logical, stack); auth != functionurl.AuthIAM {
			t.Errorf("%s Function URL auth = %q, want %q behind the Cloudflare worker", logical, auth, functionurl.AuthIAM)
		}
		if hasPublicPermission(rec, stack, logical) {
			t.Errorf("%s has a public invoke permission behind the Cloudflare worker", logical)
		}
		env := functionEnvOf(t, rec, Coordinate("shop", stack, logical).PhysicalName(component.MaxLambdaBaseNameLen))
		if _, wired := env[edge.OriginSecretVar]; wired {
			t.Errorf("%s has %s behind an edge that signs its requests", logical, edge.OriginSecretVar)
		}
		if _, fronted := env[edge.OriginFrontedVar]; fronted {
			t.Errorf("%s has %s behind an edge that presents no secret", logical, edge.OriginFrontedVar)
		}
	}
}

func TestNoneModeReachesItsEntryOverASignedURL(t *testing.T) {
	t.Parallel()

	stack := testStack(t, "prod", "web")
	rec := registerRoutedGuarded(t, apigateway.Kind, stack)

	for _, logical := range []string{"fn--web--entry", "fn--web--admin"} {
		if auth := functionURLAuthOf(t, rec, logical, stack); auth != functionurl.AuthIAM {
			t.Errorf("%s Function URL auth = %q, want %q; none mode adds no resource policy to a release function", logical, auth, functionurl.AuthIAM)
		}
		if hasPublicPermission(rec, stack, logical) {
			t.Errorf("%s has a public invoke permission; none mode signs every integration", logical)
		}
		env := functionEnvOf(t, rec, Coordinate("shop", stack, logical).PhysicalName(component.MaxLambdaBaseNameLen))
		if _, wired := env[edge.OriginSecretVar]; wired {
			t.Errorf("%s has %s, which an AWS_PROXY integration never presents", logical, edge.OriginSecretVar)
		}
		if env[edge.OriginSignedVar] == "" {
			t.Errorf("%s has no %s, so its runtime refuses every signed request", logical, edge.OriginSignedVar)
		}
	}
}

func TestAnAppThatRoutesNothingStillGuardsItsEntry(t *testing.T) {
	t.Parallel()

	cfg := guardedConfig(t, cloudfront.Kind)
	cfg.ArtifactRoot = writeTree(t, map[string]string{
		"apps/api/hosting.json": `{"version":1,"framework":"node","frameworkBuildId":"API1","rootFunction":"/"}`,
	})
	coord := storageCoordinate("prod", "shop", "api", fixedRelease(t))
	spec := servingSpec(t, cfg, "api", "express", coord)
	functions := []*contractv1.ManifestFunction{
		{LogicalName: "fn--api--entry", RouteId: "/"},
	}

	stack := testStack(t, "prod", "api")
	rec := registerGuarded(t, cfg, spec, functions, stack)

	name := naming.ResourceID(naming.KindFunction, Coordinate("shop", stack, "fn--api--entry").Name, "url")
	value := rec.inputs(functionURLToken, name)["authorizationType"]
	if !value.IsString() || value.StringValue() != functionurl.AuthNone {
		t.Errorf("entry Function URL auth = %v, want %q for the sole function of an app whose origin dispatches no path", value, functionurl.AuthNone)
	}
	env := functionEnvOf(t, rec, Coordinate("shop", stack, "fn--api--entry").PhysicalName(component.MaxLambdaBaseNameLen))
	if env[edge.OriginSecretVar] != testOriginSecret {
		t.Errorf("%s = %q, want the secret on an entry nothing else guards", edge.OriginSecretVar, env[edge.OriginSecretVar])
	}
	if _, fronted := env[edge.OriginFrontedVar]; !fronted {
		t.Errorf("the entry has no %s, want it marked fronted like any entry the secret guards", edge.OriginFrontedVar)
	}
	if _, routed := env[functionURLsEnv]; routed {
		t.Errorf("an app that routes nothing has %s", functionURLsEnv)
	}
}
