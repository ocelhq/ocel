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
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func siblingAppRoot(t *testing.T, apps ...string) string {
	t.Helper()
	files := map[string]string{}
	for _, app := range apps {
		files["apps/"+app+"/routing-manifest.json"] = routedManifest
		files["apps/"+app+"/serve.json"] = `{"framework":"next","buildId":"WEB1","edgeRouting":true,"entry":"/"}`
		files["apps/"+app+"/static/"+app+".txt"] = "an asset only " + app + " ships"
	}
	return writeTree(t, files)
}

type recordingProgress struct {
	mu    sync.Mutex
	said  []string
	spans []string
}

func (r *recordingProgress) Say(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.said = append(r.said, message)
}

func (r *recordingProgress) Detail(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.said = append(r.said, message)
}

func (r *recordingProgress) Span(name string, _, _ time.Time, err error, _ ...edge.Attr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		name += " (failed)"
	}
	r.spans = append(r.spans, name)
}

func (r *recordingProgress) reported() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append(slices.Clone(r.said), r.spans...)
}

func siblingAppSpec(t *testing.T, app string) provider.StackSpec {
	t.Helper()
	coord := storageCoordinate("prod", "shop", app, fixedRelease(t))
	return provider.StackSpec{
		Ref:  provider.StackRef{Project: "shop", Class: edge.ClassProduction, Name: coord.Stack()},
		Kind: provider.StackApp,
		Edge: fakeEdgeOf(cloudfront.Kind),
		App: &provider.AppSpec{
			App:        app,
			Framework:  appbuild.FrameworkNext,
			Entry:      "fn--" + app + "--entry",
			Deployment: "d1",
			Functions: []provider.FunctionSpec{
				{Name: "fn--" + app + "--entry", Artifact: provider.ArtifactRef{Bucket: provider.StoreFunctions, Key: app + "-entry.zip"}},
			},
			Routing:     &provider.RoutingSpec{Entry: "fn--" + app + "--entry", Manifest: []byte(routedManifest)},
			ISR:         &provider.ISRSpec{Prefix: isrPrefixOf(coord), TagNamespace: "tag:shop"},
			Bytecode:    &provider.BytecodeSpec{Prefix: bytecodePrefixOf(coord)},
			AssetPrefix: coord.AssetKey(""),
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
	cfg.Objects = &fakeArtifactStore{exists: map[string]bool{}}
	cfg.CacheStoreBucket = "isr"
	cfg.CacheStoreObjects = &fakeArtifactStore{exists: map[string]bool{}}

	engine := &mockedEngine{outputs: siblingAppOutputs(apps...)}
	stacks := standingUp(cfg, engine)

	var wg sync.WaitGroup
	failures := make([]error, len(apps))
	results := make([]provider.StackResult, len(apps))
	reports := make([]*recordingProgress, len(apps))
	for slot, app := range apps {
		reports[slot] = &recordingProgress{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[slot], failures[slot] = stacks.Provision(context.Background(), siblingAppSpec(t, app), reports[slot])
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

		want := provider.Function{
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
