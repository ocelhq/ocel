package deploy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
)

func plannedAppStack(t *testing.T) (Config, providerkit.StackPlan) {
	t.Helper()
	cfg := routedConfig(t, cloudfront.Kind)
	cfg.Env = "prod"
	cfg.ArtifactBucket = "artifacts-bucket"
	cfg.StateTable = "ocel-state"
	cfg.StateTableARN = "arn:aws:dynamodb:us-east-1:123456789012:table/ocel-state"

	coord := routedCoordinate(t)
	stack := coord.Stack()
	plan := providerkit.StackPlan{
		Ref:  providerkit.StackRef{Project: "shop", Class: providerkit.ClassProduction, Name: stack},
		Kind: providerkit.StackApp,
		Edge: fakeEdgeOf(cloudfront.Kind),
		App: &providerkit.AppPlan{
			App:        "web",
			Framework:  frameworkNext,
			Entry:      "fn--web--entry",
			Deployment: "d1",
			Functions: []providerkit.FunctionSpec{
				{Name: "fn--web--entry", Artifact: providerkit.ArtifactRef{Bucket: providerkit.StoreFunctions, Key: "entry.zip"}},
				{Name: "fn--web--admin", Route: "/admin", Artifact: providerkit.ArtifactRef{Bucket: providerkit.StoreFunctions, Key: "admin.zip"}},
			},
			Routing:     &providerkit.RoutingPlan{Entry: "fn--web--entry", Manifest: []byte(routedManifest)},
			ISR:         &providerkit.ISRPlan{Prefix: "shop/prod/web/r1/isr", TagNamespace: "tag:shop"},
			Bytecode:    &providerkit.BytecodePlan{Prefix: "shop/prod/web/r1/bytecode"},
			AssetPrefix: coord.AssetKey(""),
			Membrane:    providerkit.ArtifactRef{Bucket: providerkit.StoreFunctions, Key: providerkit.MembraneKey("abc123")},
		},
	}
	return cfg, plan
}

func TestAnAppStackStandsUpFromThePlanAlone(t *testing.T) {
	t.Parallel()

	cfg, plan := plannedAppStack(t)
	release := releasing(t, cfg)
	work, err := release.appWork(plan, nil)
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}

	rec := &inputRecorder{}
	stack := plan.Ref.Name
	program := func(pctx *pulumi.Context) error { return work.run(pctx, nil) }
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", stack.String(), rec)); err != nil {
		t.Fatalf("run the app program: %v", err)
	}

	entry := functionEnvOf(t, rec, functionCoordinate("shop", stack, "fn--web--entry").PhysicalName(maxLambdaBaseNameLen))
	if entry[edgeKindEnv] != string(cloudfront.Kind) {
		t.Errorf("%s = %q, want the edge kind the deploy chose", edgeKindEnv, entry[edgeKindEnv])
	}
	if entry[routingManifestEnv] != routingManifestInTask {
		t.Errorf("%s = %q, want the routing manifest the plan carried", routingManifestEnv, entry[routingManifestEnv])
	}
	if entry[deploymentIDEnv] != "d1" {
		t.Errorf("%s = %q, want the deployment the plan named", deploymentIDEnv, entry[deploymentIDEnv])
	}
	if entry["OCEL_ISR_PREFIX"] != plan.App.ISR.Prefix {
		t.Errorf("OCEL_ISR_PREFIX = %q, want the ledger prefix the plan carried", entry["OCEL_ISR_PREFIX"])
	}
	if entry["OCEL_ISR_TAG_NAMESPACE"] != plan.App.ISR.TagNamespace {
		t.Errorf("OCEL_ISR_TAG_NAMESPACE = %q, want the namespace the plan carried", entry["OCEL_ISR_TAG_NAMESPACE"])
	}
	var siblings map[string]string
	if err := json.Unmarshal([]byte(entry[functionURLsEnv]), &siblings); err != nil {
		t.Fatalf("%s = %q, want a routeId-to-URL object: %v", functionURLsEnv, entry[functionURLsEnv], err)
	}
	if len(siblings) != 1 || siblings["/admin"] == "" {
		t.Errorf("sibling urls = %v, want the sibling the plan's routes name", siblings)
	}
}

func TestThePlannedAppBootsThroughTheMembraneItWasHandedAndNoOther(t *testing.T) {
	t.Parallel()

	cfg, plan := plannedAppStack(t)
	release := releasing(t, cfg)
	work, err := release.appWork(plan, nil)
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}
	layer := work.functions.Layer
	if layer.Bucket != cfg.ArtifactBucket {
		t.Errorf("membrane bucket = %q, want the account's artifact bucket", layer.Bucket)
	}
	if layer.Key != plan.App.Membrane.Key {
		t.Errorf("membrane key = %q, want the one the plan named", layer.Key)
	}
	if layer.SHA256 != "abc123" {
		t.Errorf("membrane source hash = %q, want the digest its key is addressed by", layer.SHA256)
	}
}

func TestAPlannedAppWithNoISRAsksForNoRevalidationLedger(t *testing.T) {
	t.Parallel()

	cfg, plan := plannedAppStack(t)
	plan.App.ISR = nil
	release := releasing(t, cfg)
	work, err := release.appWork(plan, nil)
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}
	if work.functions.ISR != nil {
		t.Errorf("ISR = %+v for an app that revalidates nothing, want none", work.functions.ISR)
	}
}

func TestAPlannedAppGuardsItsOriginOnlyWithASecretToDemand(t *testing.T) {
	t.Parallel()

	cfg, plan := plannedAppStack(t)
	plan.App.Guard = &providerkit.OriginGuard{Entry: "fn--web--entry"}
	release := releasing(t, cfg)

	if _, err := release.appWork(plan, nil); err == nil || !strings.Contains(err.Error(), "ocel bootstrap") {
		t.Fatalf("appWork() = %v, want it to say the bootstrap holds no origin secret", err)
	}

	cfg.OriginSecret = "s3cret"
	work, err := releasing(t, cfg).appWork(plan, nil)
	if err != nil {
		t.Fatalf("appWork() with a secret = %v", err)
	}
	if work.functions.Guard == nil || work.functions.Guard.Entry != "fn--web--entry" {
		t.Errorf("Guard = %+v, want the entry route the plan named", work.functions.Guard)
	}
}

func TestAPlannedAppTakesItsGrantsFromTheLinksItWasGranted(t *testing.T) {
	t.Parallel()

	cfg, plan := plannedAppStack(t)
	plan.App.Grants = []providerkit.Link{{
		Type: providerkit.LinkBucket,
		Name: "bucket--uploads",
		Grants: []providerkit.Grant{{
			Label:     "objects",
			Actions:   []string{"s3:GetObject"},
			Resources: []string{"arn:aws:s3:::uploads/*"},
		}},
	}}
	work, err := releasing(t, cfg).appWork(plan, nil)
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}
	if len(work.role.LinkPolicies) != 1 || work.role.LinkPolicies[0].Link != "bucket--uploads" {
		t.Fatalf("link policies = %+v, want one for the link the app was granted", work.role.LinkPolicies)
	}
	if !strings.Contains(work.role.LinkPolicies[0].Policy, "s3:GetObject") {
		t.Errorf("policy = %q, want the actions the grant carried", work.role.LinkPolicies[0].Policy)
	}
}

func TestAnAppStackWhosePlanCarriesNoAppIsRefusedRatherThanStoodUpEmpty(t *testing.T) {
	t.Parallel()

	release := releasing(t, Config{})
	plan := providerkit.StackPlan{
		Ref:  providerkit.StackRef{Project: "shop", Name: naming.AppStack("prod", "web", naming.NewRelease("d1", ""))},
		Kind: providerkit.StackApp,
	}
	err := pulumi.RunErr(func(pctx *pulumi.Context) error { return release.Run(pctx, plan) },
		pulumi.WithMocks("shop", plan.Ref.Name.String(), &inputRecorder{}))
	if err == nil || !strings.Contains(err.Error(), "carries none") {
		t.Fatalf("Run() = %v, want it refused: an app stack with no app stands nothing up", err)
	}
}

func TestTheWarmerAndTheEmbedderReachTheFunctionsThePlanStoodUp(t *testing.T) {
	t.Parallel()

	cfg, plan := plannedAppStack(t)
	release := releasing(t, cfg)
	if _, err := release.appWork(plan, nil); err != nil {
		t.Fatalf("appWork() = %v", err)
	}
	release.served.realized("fn--web--entry", "shop-prod-web-entry")

	held, known := release.served.byPhysicalName("shop-prod-web-entry")
	if !known {
		t.Fatal("the warmer cannot reach a function the plan stood up")
	}
	if held.App != "web" || held.Logical != "fn--web--entry" {
		t.Errorf("realized %+v, want the app and logical name the plan named", held)
	}
	if held.Bytecode == nil || held.Bytecode.Prefix != plan.App.Bytecode.Prefix {
		t.Errorf("bytecode = %+v, want the cache prefix the plan carried", held.Bytecode)
	}
	if err := release.Warm(context.Background(), []string{"shop-prod-web-entry"}, nil); err != nil {
		t.Errorf("Warm() with no invoker configured = %v, want it to pass over", err)
	}
}

func TestAnAppIsRefusedBeforeItsRoleIsBuilt(t *testing.T) {
	t.Parallel()

	t.Run("a plain name the Lambda runtime owns", func(t *testing.T) {
		t.Parallel()

		cfg, plan := plannedAppStack(t)
		plan.App.Values.Plain = map[string]string{"AWS_REGION": "us-west-2"}

		_, err := releasing(t, cfg).appWork(plan, nil)
		if err == nil || !strings.Contains(err.Error(), "AWS_REGION") {
			t.Fatalf("appWork() = %v, want the deploy refused before the role is built", err)
		}
	})
}

func TestASuppressedReleaseTellsAFunctionNothingWasProvisioned(t *testing.T) {
	plan := providerkit.StackPlan{
		Kind: providerkit.StackApp,
		App: &providerkit.AppPlan{
			App:    "web",
			Values: providerkit.AppValues{Phase: providerkit.PhaseResourcesSuppressed},
		},
	}

	env := releasing(t, Config{}).appEnv(plan, appBundle{}, sessionScope{})

	if got := env[providerkit.PhaseEnvName]; got != providerkit.PhaseResourcesSuppressed {
		t.Errorf("%s = %q, want %q: a function reads the phase off its environment like a container does",
			providerkit.PhaseEnvName, got, providerkit.PhaseResourcesSuppressed)
	}
}

func TestAReleaseThatProvisionsNamesNoPhase(t *testing.T) {
	plan := providerkit.StackPlan{Kind: providerkit.StackApp, App: &providerkit.AppPlan{App: "web"}}

	env := releasing(t, Config{}).appEnv(plan, appBundle{}, sessionScope{})

	if got, held := env[providerkit.PhaseEnvName]; held {
		t.Errorf("%s = %q, want a release that provisions to name no phase at all", providerkit.PhaseEnvName, got)
	}
}
