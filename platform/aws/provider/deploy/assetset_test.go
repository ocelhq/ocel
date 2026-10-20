package deploy

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/site"
)

func assetShippingConfig(t *testing.T, app string) Config {
	t.Helper()
	cfg := routedConfig(t, cloudfront.Kind)
	cfg.ArtifactRoot = siblingAppRoot(t, app)
	cfg.Env = "prod"
	cfg.Buckets[naming.StorageArtifacts] = "artifacts-bucket"
	cfg.StateTable = "ocel-state"
	cfg.StateTableARN = "arn:aws:dynamodb:us-east-1:123456789012:table/ocel-state"
	cfg.BackendURL = "s3://ocel-state/shop"
	cfg.Passphrase = "a-passphrase"
	cfg.PulumiProject = "ocel-shop"
	cfg.Region = "us-east-1"
	cfg.ImageOptimizerURL = ""
	cfg.EdgeAccessKeyID, cfg.EdgeSecretKey = "AKIAEDGE", "edge-secret"
	return cfg
}

func assetSetRows(plan provider.Plan) []string {
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
	assets := &fakeArtifactStore{exists: map[string]bool{}}
	cfg := assetShippingConfig(t, "web")
	cfg.Objects = assets

	engine := &mockedEngine{outputs: siblingAppOutputs("web")}
	stacks := stacksWith(cfg, engine)
	spec := siblingAppSpec(t, "web")

	planned, err := stacks.Plan(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Plan() of an app shipping assets = %v", err)
	}
	want := []string{site.PrerenderAssetSetName, site.StaticAssetSetName}
	if got := assetSetRows(planned); !slices.Equal(got, want) {
		t.Errorf("Plan() showed asset-set rows %v, want %v: an upload is a mutation the plan has to name", got, want)
	}
	if len(assets.puts) != 0 {
		t.Errorf("Plan() wrote %v, want nothing: a plan says what an apply would do before it does it", assets.puts)
	}

	if _, err := stacks.Provision(ctx, siblingAppSpec(t, "web"), nil); err != nil {
		t.Fatalf("Provision() of the app whose plan showed the asset sets = %v", err)
	}
	if !slices.ContainsFunc(assets.puts, func(key string) bool { return strings.HasSuffix(key, "/web.txt") }) {
		t.Errorf("the edge bucket has %v, want the static asset the plan's row promised", assets.puts)
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

func TestNoFunctionIsProvisionedBeforeTheAssetsItServes(t *testing.T) {
	t.Parallel()

	cfg := assetShippingConfig(t, "web")
	cfg.Objects = &fakeArtifactStore{exists: map[string]bool{}}

	order := &registrationOrder{}
	engine := &mockedEngine{outputs: siblingAppOutputs("web"), mocks: order}
	if _, err := stacksWith(cfg, engine).Provision(context.Background(), siblingAppSpec(t, "web"), nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}

	sets, function := order.at(site.AssetSetToken), order.at("aws:lambda/function:Function")
	if sets == -1 || function == -1 {
		t.Fatalf("the run registered %v, want both an asset set and a function", order.kinds)
	}
	if sets > function {
		t.Errorf("the function was provisioned before its asset set (%v), want the assets in place before anything serves them", order.kinds)
	}
}

func TestAnAssetSetIsOneRowWhateverTheFileCount(t *testing.T) {
	t.Parallel()

	files := map[string]string{
		"apps/web/next-route-table.json": routedManifest,
		"apps/web/hosting.json":          `{"version":1,"framework":"next","frameworkBuildId":"WEB1","rootFunction":"/","routeTable":"next","static":{"immutablePrefixes":["/_next/static/"],"mustRevalidatePrefixes":["/_next/static/service-worker/"]}}`,
	}
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		files["apps/web/static/"+name+".txt"] = name
	}
	cfg := assetShippingConfig(t, "web")
	cfg.ArtifactRoot = writeTree(t, files)
	cfg.Objects = &fakeArtifactStore{exists: map[string]bool{}}

	engine := &mockedEngine{outputs: siblingAppOutputs("web")}
	planned, err := stacksWith(cfg, engine).Plan(context.Background(), siblingAppSpec(t, "web"), nil)
	if err != nil {
		t.Fatalf("Plan() of an app shipping many files = %v", err)
	}
	if got := assetSetRows(planned); len(got) != 2 {
		t.Errorf("Plan() showed %d asset-set rows for 8 files, want one per set: engine state grows with sets, not files", len(got))
	}
}
