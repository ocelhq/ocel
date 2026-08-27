package deploy

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
)

func createRequest(args assetSetArgs, planning bool) infer.CreateRequest[assetSetArgs] {
	return infer.CreateRequest[assetSetArgs]{Inputs: args, DryRun: planning}
}

func assetShippingConfig(t *testing.T, app string) Config {
	t.Helper()
	cfg := routedConfig(t, cloudfront.Kind)
	cfg.ArtifactRoot = siblingAppRoot(t, app)
	cfg.Env = "prod"
	cfg.ArtifactBucket = "artifacts-bucket"
	cfg.StateTable = "ocel-state"
	cfg.StateTableARN = "arn:aws:dynamodb:us-east-1:123456789012:table/ocel-state"
	cfg.BackendURL = "s3://ocel-state/shop"
	cfg.Passphrase = "a-passphrase"
	cfg.PulumiProject = "ocel-shop"
	cfg.Region = "us-east-1"
	cfg.ImageOptimizerURL = ""
	cfg.CacheStoreBucket = "isr"
	return cfg
}

func assetSetRows(plan providerkit.Plan) []string {
	var named []string
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			if strings.Contains(strings.ToLower(change.Kind), "assetset") {
				named = append(named, change.Name)
			}
		}
	}
	slices.Sort(named)
	return named
}

func TestEveryAssetSetIsARowThePlanShowsAndAnUploadTheApplyMakes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	assets, store := &fakeUploader{exists: map[string]bool{}}, &fakeUploader{exists: map[string]bool{}}
	cfg := assetShippingConfig(t, "web")
	cfg.Uploader = assets
	cfg.CacheStoreUploader = store

	engine := &mockedEngine{outputs: siblingAppOutputs("web")}
	releaser := standingUp(cfg, engine)
	plan := siblingAppPlan(t, "web")

	planned, err := releaser.Plan(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Plan() of an app shipping assets = %v", err)
	}
	want := []string{prerenderAssetSetName, staticAssetSetName}
	if got := assetSetRows(planned); !slices.Equal(got, want) {
		t.Errorf("Plan() showed asset-set rows %v, want %v: an upload is a mutation the plan has to name", got, want)
	}
	if len(assets.puts)+len(store.puts) != 0 {
		t.Errorf("Plan() wrote %v and %v, want nothing: a plan says what an apply would do before it does it",
			assets.puts, store.puts)
	}

	if _, err := releaser.Provision(ctx, siblingAppPlan(t, "web"), nil); err != nil {
		t.Fatalf("Provision() of the app whose plan showed the asset sets = %v", err)
	}
	if !slices.ContainsFunc(store.puts, func(key string) bool { return strings.HasSuffix(key, "/web.txt") }) {
		t.Errorf("the cache store holds %v, want the static asset the plan's row promised", store.puts)
	}
}

type registrationOrder struct {
	mu    sync.Mutex
	kinds []string
}

func (r *registrationOrder) NewResource(args sdk.MockResourceArgs) (string, resource.PropertyMap, error) {
	r.mu.Lock()
	r.kinds = append(r.kinds, args.TypeToken)
	r.mu.Unlock()
	return standInCloud{}.NewResource(args)
}

func (r *registrationOrder) Call(args sdk.MockCallArgs) (resource.PropertyMap, error) {
	return standInCloud{}.Call(args)
}

func (r *registrationOrder) at(token string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Index(r.kinds, token)
}

func TestNoFunctionStandsUpBeforeTheAssetsItServes(t *testing.T) {
	t.Parallel()

	cfg := assetShippingConfig(t, "web")
	cfg.Uploader = &fakeUploader{exists: map[string]bool{}}
	cfg.CacheStoreUploader = &fakeUploader{exists: map[string]bool{}}

	order := &registrationOrder{}
	engine := &mockedEngine{outputs: siblingAppOutputs("web"), mocks: order}
	if _, err := standingUp(cfg, engine).Provision(context.Background(), siblingAppPlan(t, "web"), nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}

	sets, function := order.at(assetSetToken), order.at("aws:lambda/function:Function")
	if sets == -1 || function == -1 {
		t.Fatalf("the run registered %v, want both an asset set and a function", order.kinds)
	}
	if sets > function {
		t.Errorf("the function stood up before its asset set (%v), want the assets in place before anything serves them", order.kinds)
	}
}

func TestAnAssetSetIsOneRowWhateverTheFileCount(t *testing.T) {
	t.Parallel()

	files := map[string]string{
		"apps/web/routing-manifest.json": routedManifest,
		"apps/web/serve.json":            `{"framework":"next","buildId":"WEB1","edgeRouting":true,"entry":"/"}`,
	}
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		files["apps/web/static/"+name+".txt"] = name
	}
	cfg := assetShippingConfig(t, "web")
	cfg.ArtifactRoot = writeTree(t, files)
	cfg.Uploader = &fakeUploader{exists: map[string]bool{}}
	cfg.CacheStoreUploader = &fakeUploader{exists: map[string]bool{}}

	engine := &mockedEngine{outputs: siblingAppOutputs("web")}
	planned, err := standingUp(cfg, engine).Plan(context.Background(), siblingAppPlan(t, "web"), nil)
	if err != nil {
		t.Fatalf("Plan() of an app shipping many files = %v", err)
	}
	if got := assetSetRows(planned); len(got) != 2 {
		t.Errorf("Plan() showed %d asset-set rows for 8 files, want one per set: engine state grows with sets, not files", len(got))
	}
}

func TestAStaticAssetSetIsPlannedOnceAndPushedOnce(t *testing.T) {
	t.Parallel()

	cfg := assetShippingConfig(t, "web")
	cfg.Uploader = &fakeUploader{exists: map[string]bool{}}
	cfg.CacheStoreUploader = &fakeUploader{exists: map[string]bool{}}
	coord := storageCoordinate("prod", "shop", "web", fixedRelease(t))

	first, err := staticAssetSet(cfg, "web", frameworkNext, coord)
	if err != nil {
		t.Fatalf("staticAssetSet: %v", err)
	}
	second, err := staticAssetSet(cfg, "web", frameworkNext, coord)
	if err != nil {
		t.Fatalf("staticAssetSet: %v", err)
	}
	if first.digest != second.digest {
		t.Errorf("the same build digests to %q then %q, want one digest so an unchanged set is not re-pushed", first.digest, second.digest)
	}
	if first.files == 0 {
		t.Error("the set counts no files, and the plan's row would say nothing about what it carries")
	}
}

func TestAnAssetSetTheRunNeverHeldIsRefused(t *testing.T) {
	t.Parallel()

	pending := newPendingSets()
	resource := &assetSetResource{pending: pending}
	if _, err := resource.Create(context.Background(), createRequest(assetSetArgs{
		Stack: "prod.web.rel-1", Set: staticAssetSetName,
	}, false)); err == nil {
		t.Error("Create() of a set the run holds nothing for = nil, want a refusal: the plan's row would go unwritten")
	}
}

func TestAPlannedAssetSetPushesNothing(t *testing.T) {
	t.Parallel()

	pending := newPendingSets()
	pushed := 0
	pending.hold("prod.web.rel-1", []assetSet{{
		name: staticAssetSetName,
		push: func(context.Context, providerkit.Reporter) error { pushed++; return nil },
	}}, nil)

	resource := &assetSetResource{pending: pending}
	if _, err := resource.Create(context.Background(), createRequest(assetSetArgs{
		Stack: "prod.web.rel-1", Set: staticAssetSetName,
	}, true)); err != nil {
		t.Fatalf("Create() during a plan = %v", err)
	}
	if pushed != 0 {
		t.Errorf("a planned set pushed %d times, want none", pushed)
	}

	if _, err := resource.Create(context.Background(), createRequest(assetSetArgs{
		Stack: "prod.web.rel-1", Set: staticAssetSetName,
	}, false)); err != nil {
		t.Fatalf("Create() during an apply = %v", err)
	}
	if pushed != 1 {
		t.Errorf("an applied set pushed %d times, want the one the plan showed", pushed)
	}
}
