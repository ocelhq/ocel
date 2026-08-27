package deploy

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
)

func siblingAppRoot(t *testing.T, apps ...string) string {
	t.Helper()
	files := map[string]string{}
	for _, app := range apps {
		files["apps/"+app+"/routing-manifest.json"] = routedManifest
		files["apps/"+app+"/serve.json"] = `{"framework":"next","buildId":"WEB1","edgeRouting":true,"entry":"/"}`
	}
	return writeTree(t, files)
}

func siblingAppPlan(t *testing.T, app string) providerkit.StackPlan {
	t.Helper()
	coord := storageCoordinate("prod", "shop", app, fixedRelease(t))
	return providerkit.StackPlan{
		Ref:  providerkit.StackRef{Project: "shop", Class: providerkit.ClassProduction, Name: coord.Stack()},
		Kind: providerkit.StackApp,
		Edge: fakeEdgeOf(cloudfront.Kind),
		App: &providerkit.AppPlan{
			App:        app,
			Framework:  frameworkNext,
			Entry:      "fn--" + app + "--entry",
			Deployment: "d1",
			Functions: []providerkit.FunctionSpec{
				{Name: "fn--" + app + "--entry", Artifact: providerkit.ArtifactRef{Bucket: providerkit.StoreFunctions, Key: app + "-entry.zip"}},
			},
			Routing:     &providerkit.RoutingPlan{Entry: "fn--" + app + "--entry", Manifest: []byte(routedManifest)},
			ISR:         &providerkit.ISRPlan{Prefix: isrPrefixOf(coord), TagNamespace: "tag:shop"},
			Bytecode:    &providerkit.BytecodePlan{Prefix: bytecodePrefixOf(coord)},
			AssetPrefix: coord.AssetKey(""),
			Membrane:    providerkit.ArtifactRef{Bucket: providerkit.StoreFunctions, Key: providerkit.MembraneKey("abc123")},
		},
	}
}

func siblingAppOutputs(apps ...string) auto.OutputMap {
	outputs := auto.OutputMap{}
	for _, app := range apps {
		outputs["fn--"+app+"--entry"] = auto.OutputValue{Value: map[string]any{
			outputKeyFunctionURL:  "https://" + app + ".lambda-url.us-east-1.on.aws/",
			outputKeyFunctionName: "shop-prod-" + app + "-entry",
		}}
	}
	return outputs
}

func TestOneReleaserStandsUpSiblingAppStacksAtOnce(t *testing.T) {
	t.Parallel()

	apps := []string{"web", "admin", "docs"}
	cfg := routedConfig(t, cloudfront.Kind)
	cfg.ArtifactRoot = siblingAppRoot(t, apps...)
	cfg.Env = "prod"
	cfg.ArtifactBucket = "artifacts-bucket"
	cfg.StateTable = "ocel-state"
	cfg.StateTableARN = "arn:aws:dynamodb:us-east-1:123456789012:table/ocel-state"
	cfg.BackendURL = "s3://ocel-state/shop"
	cfg.Passphrase = "a-passphrase"
	cfg.PulumiProject = "ocel-shop"
	cfg.Region = "us-east-1"
	cfg.ImageOptimizerURL = ""

	engine := &mockedEngine{outputs: siblingAppOutputs(apps...)}
	releaser := newReleaser(fixed(cfg), &Realized{}, engine)

	var wg sync.WaitGroup
	failures := make([]error, len(apps))
	for slot, app := range apps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, failures[slot] = releaser.Provision(context.Background(), siblingAppPlan(t, app), nil)
		}()
	}
	wg.Wait()

	for slot, err := range failures {
		if err != nil {
			t.Fatalf("Provision(%s) = %v", apps[slot], err)
		}
	}

	ran := engine.stacks()
	if len(ran) != len(apps) {
		t.Fatalf("the engine ran %v, want one stack per app", ran)
	}
	for _, app := range apps {
		stack := naming.AppStack("prod", app, fixedRelease(t)).String()
		if !slices.Contains(ran, stack) {
			t.Errorf("the engine never ran %s: %v", stack, ran)
		}
		held, known := releaser.served.byPhysicalName("shop-prod-" + app + "-entry")
		if !known || held.App != app {
			t.Errorf("the served index lost %s's entry function under concurrency: %+v", app, held)
		}
	}
}
