package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/arch"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func appStackSpec(t *testing.T) (Config, provider.StackSpec) {
	t.Helper()
	cfg := routedConfig(t, cloudfront.Kind)
	cfg.Env = "prod"
	cfg.ArtifactBucket = "artifacts-bucket"
	cfg.StateTable = "ocel-state"
	cfg.StateTableARN = "arn:aws:dynamodb:us-east-1:123456789012:table/ocel-state"

	coord := routedCoordinate(t)
	stack := coord.Stack()
	spec := provider.StackSpec{
		Ref:  provider.StackRef{Project: "shop", Class: edge.ClassProduction, Name: stack},
		Kind: provider.StackApp,
		Edge: fakeEdgeOf(cloudfront.Kind),
		App: &provider.AppSpec{
			App:        "web",
			Framework:  appbuild.FrameworkNext,
			Entry:      "fn--web--entry",
			Deployment: "d1",
			Functions: []provider.FunctionSpec{
				{Name: "fn--web--entry", Artifact: provider.ArtifactRef{Bucket: provider.StoreFunctions, Key: "entry.zip"}},
				{Name: "fn--web--admin", Route: "/admin", Artifact: provider.ArtifactRef{Bucket: provider.StoreFunctions, Key: "admin.zip"}},
			},
			Routing:     &provider.RoutingSpec{Entry: "fn--web--entry", Manifest: []byte(routedManifest)},
			ISR:         &provider.ISRSpec{Prefix: "shop/prod/web/r1/isr", TagNamespace: "tag:shop"},
			Bytecode:    &provider.BytecodeSpec{Prefix: "shop/prod/web/r1/bytecode"},
			AssetPrefix: coord.AssetKey(""),
		},
	}
	return cfg, spec
}

func TestAnAppStackStandsUpFromTheSpecAlone(t *testing.T) {
	t.Parallel()

	cfg, spec := appStackSpec(t)
	release := releasing(t, cfg)
	work, err := release.appWork(spec, nil)
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}

	rec := &inputRecorder{}
	stack := spec.Ref.Name
	program := func(pctx *pulumi.Context) error { return work.run(pctx, nil) }
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", stack.String(), rec)); err != nil {
		t.Fatalf("run the app program: %v", err)
	}

	entry := functionEnvOf(t, rec, functionCoordinate("shop", stack, "fn--web--entry").PhysicalName(maxLambdaBaseNameLen))
	if entry[edgeKindEnv] != string(cloudfront.Kind) {
		t.Errorf("%s = %q, want the edge kind the deploy chose", edgeKindEnv, entry[edgeKindEnv])
	}
	if entry[routingManifestEnv] != routingManifestInTask {
		t.Errorf("%s = %q, want the routing manifest the spec carried", routingManifestEnv, entry[routingManifestEnv])
	}
	if entry[deploymentIDEnv] != "d1" {
		t.Errorf("%s = %q, want the deployment the spec named", deploymentIDEnv, entry[deploymentIDEnv])
	}
	if entry["OCEL_ISR_PREFIX"] != spec.App.ISR.Prefix {
		t.Errorf("OCEL_ISR_PREFIX = %q, want the ledger prefix the spec carried", entry["OCEL_ISR_PREFIX"])
	}
	if entry["OCEL_ISR_TAG_NAMESPACE"] != spec.App.ISR.TagNamespace {
		t.Errorf("OCEL_ISR_TAG_NAMESPACE = %q, want the namespace the spec carried", entry["OCEL_ISR_TAG_NAMESPACE"])
	}
	var siblings map[string]string
	if err := json.Unmarshal([]byte(entry[functionURLsEnv]), &siblings); err != nil {
		t.Fatalf("%s = %q, want a routeId-to-URL object: %v", functionURLsEnv, entry[functionURLsEnv], err)
	}
	if len(siblings) != 1 || siblings["/admin"] == "" {
		t.Errorf("sibling urls = %v, want the sibling the spec's routes name", siblings)
	}
}

func TestThePlannedAppBootsThroughTheRuntimeItWasHandedAndNoOther(t *testing.T) {
	t.Parallel()

	cfg, spec := appStackSpec(t)
	release := releasing(t, cfg)
	work, err := release.appWork(spec, nil)
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}
	if len(work.functions.Layers) != 1 {
		t.Fatalf("the app boots through %v, want the one runtime its functions' architecture names", work.functions.Layers)
	}
	if got, want := work.functions.Layers[arch.X8664], cfg.RuntimeLayers[arch.X8664]; got != want {
		t.Errorf("runtime layer = %q, want %q: the version the account's bootstrap published", got, want)
	}
}

func TestAnAppIsRefusedRatherThanDeployedAgainstARuntimeTheAccountDoesNotHold(t *testing.T) {
	t.Parallel()

	cfg, spec := appStackSpec(t)
	cfg.RuntimeLayers = nil
	release := releasing(t, cfg)
	_, err := release.appWork(spec, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("appWork() = %v, want a %s refusal naming the bootstrap to re-run", err, refusal.CodeNotReady)
	}
	if !strings.Contains(refused.Message, provider.BootstrapCommand(cfg.Class)) {
		t.Errorf("the refusal reads %q, want it to name `%s`", refused.Message, provider.BootstrapCommand(cfg.Class))
	}
}

func TestAPlannedAppWithNoISRAsksForNoRevalidationLedger(t *testing.T) {
	t.Parallel()

	cfg, spec := appStackSpec(t)
	spec.App.ISR = nil
	release := releasing(t, cfg)
	work, err := release.appWork(spec, nil)
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}
	if work.functions.ISR != nil {
		t.Errorf("ISR = %+v for an app that revalidates nothing, want none", work.functions.ISR)
	}
}

func TestAPlannedAppGuardsItsOriginOnlyWithASecretToDemand(t *testing.T) {
	t.Parallel()

	cfg, spec := appStackSpec(t)
	spec.App.Guard = &provider.OriginGuard{Entry: "fn--web--entry"}
	release := releasing(t, cfg)

	if _, err := release.appWork(spec, nil); err == nil || !strings.Contains(err.Error(), "ocel bootstrap") {
		t.Fatalf("appWork() = %v, want it to say the bootstrap holds no origin secret", err)
	}

	cfg.OriginSecret = "s3cret"
	work, err := releasing(t, cfg).appWork(spec, nil)
	if err != nil {
		t.Fatalf("appWork() with a secret = %v", err)
	}
	if work.functions.Guard == nil || work.functions.Guard.Entry != "fn--web--entry" {
		t.Errorf("Guard = %+v, want the entry route the spec named", work.functions.Guard)
	}
}

func TestAPlannedAppTakesItsGrantsFromTheBindingsItWasGranted(t *testing.T) {
	t.Parallel()

	cfg, spec := appStackSpec(t)
	spec.App.Grants = []provider.Binding{{
		Type: provider.BindingBucket,
		Name: "bucket--uploads",
		Grants: []provider.Grant{{
			Label:     "objects",
			Actions:   []string{"s3:GetObject"},
			Resources: []string{"arn:aws:s3:::uploads/*"},
		}},
	}}
	work, err := releasing(t, cfg).appWork(spec, nil)
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}
	if len(work.role.BindingPolicies) != 1 || work.role.BindingPolicies[0].Binding != "bucket--uploads" {
		t.Fatalf("binding policies = %+v, want one for the binding the app was granted", work.role.BindingPolicies)
	}
	if !strings.Contains(work.role.BindingPolicies[0].Policy, "s3:GetObject") {
		t.Errorf("policy = %q, want the actions the grant carried", work.role.BindingPolicies[0].Policy)
	}
}

func TestAnAppStackWhoseSpecCarriesNoAppIsRefusedRatherThanStoodUpEmpty(t *testing.T) {
	t.Parallel()

	release := releasing(t, Config{})
	spec := provider.StackSpec{
		Ref:  provider.StackRef{Project: "shop", Name: naming.AppStack("prod", "web", naming.NewRelease("d1", ""))},
		Kind: provider.StackApp,
	}
	err := pulumi.RunErr(func(pctx *pulumi.Context) error { return release.Run(pctx, spec) },
		pulumi.WithMocks("shop", spec.Ref.Name.String(), &inputRecorder{}))
	if err == nil || !strings.Contains(err.Error(), "carries none") {
		t.Fatalf("Run() = %v, want it refused: an app stack with no app stands nothing up", err)
	}
}

func TestTheWarmerAndTheEmbedderReachTheFunctionsTheStackStoodUp(t *testing.T) {
	t.Parallel()

	cfg, spec := appStackSpec(t)
	release := releasing(t, cfg)
	if _, err := release.appWork(spec, nil); err != nil {
		t.Fatalf("appWork() = %v", err)
	}
	release.served.realized("fn--web--entry", "shop-prod-web-entry")

	held, known := release.served.byPhysicalName("shop-prod-web-entry")
	if !known {
		t.Fatal("the warmer cannot reach a function the spec stood up")
	}
	if held.App != "web" || held.Logical != "fn--web--entry" {
		t.Errorf("realized %+v, want the app and logical name the spec named", held)
	}
	if held.Bytecode == nil || held.Bytecode.Prefix != spec.App.Bytecode.Prefix {
		t.Errorf("bytecode = %+v, want the cache prefix the spec carried", held.Bytecode)
	}
	if err := release.Warm(context.Background(), []string{"shop-prod-web-entry"}, nil); err != nil {
		t.Errorf("Warm() with no invoker configured = %v, want it to pass over", err)
	}
}

func TestAnAppIsRefusedBeforeItsRoleIsBuilt(t *testing.T) {
	t.Parallel()

	t.Run("a plain name the Lambda runtime owns", func(t *testing.T) {
		t.Parallel()

		cfg, spec := appStackSpec(t)
		spec.App.Values.Plain = map[string]string{"AWS_REGION": "us-west-2"}

		_, err := releasing(t, cfg).appWork(spec, nil)
		if err == nil || !strings.Contains(err.Error(), "AWS_REGION") {
			t.Fatalf("appWork() = %v, want the deploy refused before the role is built", err)
		}
	})
}

func TestAReleaseThatProvisionsNamesNoPhase(t *testing.T) {
	spec := provider.StackSpec{Kind: provider.StackApp, App: &provider.AppSpec{App: "web"}}

	env := releasing(t, Config{}).appEnv(spec, appBundle{}, sessionScope{})

	if got, held := env[constants.PhaseEnvName]; held {
		t.Errorf("%s = %q, want a release that provisions to name no phase at all", constants.PhaseEnvName, got)
	}
}
