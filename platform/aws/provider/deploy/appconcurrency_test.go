package deploy

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
		files["apps/"+app+"/serve.json"] = `{"runtime":"next","buildId":"WEB1","edgeRouting":true,"entry":"/"}`
		files["apps/"+app+"/static/"+app+".txt"] = "an asset only " + app + " ships"
	}
	return writeTree(t, files)
}

type recordingReporter struct {
	mu    sync.Mutex
	said  []string
	spans []string
}

func (r *recordingReporter) Say(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.said = append(r.said, message)
}

func (r *recordingReporter) Detail(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.said = append(r.said, message)
}

func (r *recordingReporter) Span(name string, _, _ time.Time, err error, _ ...providerkit.Attr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		name += " (failed)"
	}
	r.spans = append(r.spans, name)
}

func (r *recordingReporter) reported() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append(slices.Clone(r.said), r.spans...)
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
			Runtime:    runtimeNext,
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
	cfg.Uploader = &fakeUploader{exists: map[string]bool{}}
	cfg.CacheStoreBucket = "isr"
	cfg.CacheStoreUploader = &fakeUploader{exists: map[string]bool{}}

	engine := &mockedEngine{outputs: siblingAppOutputs(apps...)}
	releaser := standingUp(cfg, engine)

	var wg sync.WaitGroup
	failures := make([]error, len(apps))
	results := make([]providerkit.StackResult, len(apps))
	reports := make([]*recordingReporter, len(apps))
	for slot, app := range apps {
		reports[slot] = &recordingReporter{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[slot], failures[slot] = releaser.Provision(context.Background(), siblingAppPlan(t, app), reports[slot])
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
	for slot, app := range apps {
		stack := naming.AppStack("prod", app, fixedRelease(t)).String()
		if !slices.Contains(ran, stack) {
			t.Errorf("the engine never ran %s: %v", stack, ran)
		}

		want := providerkit.Function{
			Name:     "fn--" + app + "--entry",
			Physical: "shop-prod-" + app + "-entry",
			URL:      "https://" + app + ".lambda-url.us-east-1.on.aws/",
		}
		if got := results[slot].Functions; len(got) != 1 || got[0] != want {
			t.Errorf("Provision(%s) returned %+v, want the one function %+v: a sibling standing up beside it may not change what it hands back", app, got, want)
		}

		reported := reports[slot].reported()
		if !slices.ContainsFunc(reported, func(line string) bool { return strings.Contains(line, app) }) {
			t.Errorf("%s's reporter heard %v, want its own app named: the surface has to say which app it is speaking for", app, reported)
		}
		for _, sibling := range apps {
			if sibling == app {
				continue
			}
			if slices.ContainsFunc(reported, func(line string) bool { return strings.Contains(line, sibling) }) {
				t.Errorf("%s's reporter heard %v, want nothing of %s: siblings standing up at once may not report into each other's stage", app, reported, sibling)
			}
		}
	}
}
