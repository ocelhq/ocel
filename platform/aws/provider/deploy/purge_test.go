package deploy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type sweepRecorder struct{ swept []string }

func (r *sweepRecorder) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	r.swept = append(r.swept, aws.ToString(in.Bucket)+"|"+aws.ToString(in.Prefix))
	return &s3.ListObjectsV2Output{}, nil
}

func (r *sweepRecorder) DeleteObjects(context.Context, *s3.DeleteObjectsInput, ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	return &s3.DeleteObjectsOutput{}, nil
}

func (r *sweepRecorder) HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return nil, errors.New("not implemented")
}

func (r *sweepRecorder) PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return nil, errors.New("not implemented")
}

var _ ArtifactUploader = (*sweepRecorder)(nil)
var _ PrefixDeleter = (*sweepRecorder)(nil)

type valueRecorder struct {
	purged []string
	err    error
}

func (r *valueRecorder) Purge(_ context.Context, slug string) (int, error) {
	r.purged = append(r.purged, slug)
	if r.err != nil {
		return 0, r.err
	}
	return 7, nil
}

var _ ValueStore = (*valueRecorder)(nil)

func purgeConfig(env string, account, cache *sweepRecorder) Config {
	return Config{
		Env:                env,
		AssetBucket:        "asset-bucket",
		ArtifactBucket:     "artifact-bucket",
		Uploader:           account,
		CacheStoreBucket:   "cache-bucket",
		CacheStoreUploader: cache,
	}
}

func TestPurgeProjectAssets(t *testing.T) {
	t.Run("one prefix per environment reaches every bucket the deploy wrote to", func(t *testing.T) {
		awsSide, cacheSide := &sweepRecorder{}, &sweepRecorder{}

		if err := purgeProjectAssets(context.Background(), purgeConfig("prod", awsSide, cacheSide), "shop", []string{"prod"}); err != nil {
			t.Fatalf("purgeProjectAssets: %v", err)
		}

		wantAWS := []string{
			"asset-bucket|prod/shop/",
			"artifact-bucket|prod/shop/",
		}
		if !reflect.DeepEqual(awsSide.swept, wantAWS) {
			t.Errorf("account-side sweeps = %v, want %v", awsSide.swept, wantAWS)
		}
		wantCache := []string{"cache-bucket|prod/shop/"}
		if !reflect.DeepEqual(cacheSide.swept, wantCache) {
			t.Errorf("cache-store sweeps = %v, want %v", cacheSide.swept, wantCache)
		}
	})

	t.Run("a preview teardown leaves production's bytes alone", func(t *testing.T) {
		awsSide, cacheSide := &sweepRecorder{}, &sweepRecorder{}

		if err := purgePreviewAssets(context.Background(), purgeConfig("prod", awsSide, cacheSide), "shop", []string{"pr-7"}); err != nil {
			t.Fatalf("purgePreviewAssets: %v", err)
		}

		for _, swept := range append(awsSide.swept, cacheSide.swept...) {
			if !strings.Contains(swept, "|pr-7/") {
				t.Errorf("preview teardown swept %q, which is not the preview's own prefix", swept)
			}
		}
	})
}

func TestPurgePreviewAssets(t *testing.T) {
	t.Run("sweeps every pointer it is given and nothing else", func(t *testing.T) {
		awsSide, cacheSide := &sweepRecorder{}, &sweepRecorder{}

		err := purgePreviewAssets(context.Background(), purgeConfig("pr-1", awsSide, cacheSide), "shop", []string{"pr-1", "staging"})
		if err != nil {
			t.Fatalf("purgePreviewAssets: %v", err)
		}

		wantAWS := []string{
			"asset-bucket|pr-1/shop/",
			"artifact-bucket|pr-1/shop/",
			"asset-bucket|staging/shop/",
			"artifact-bucket|staging/shop/",
		}
		if !reflect.DeepEqual(awsSide.swept, wantAWS) {
			t.Errorf("account-side sweeps = %v, want %v", awsSide.swept, wantAWS)
		}
		wantCache := []string{
			"cache-bucket|pr-1/shop/",
			"cache-bucket|staging/shop/",
		}
		if !reflect.DeepEqual(cacheSide.swept, wantCache) {
			t.Errorf("cache-store sweeps = %v, want %v", cacheSide.swept, wantCache)
		}
	})

	t.Run("missing bucket is a no-op", func(t *testing.T) {
		rec := &sweepRecorder{}

		if err := purgeProjectAssets(context.Background(), Config{Uploader: rec}, "shop", []string{"prod"}); err != nil {
			t.Fatalf("purgeProjectAssets: %v", err)
		}
		if rec.swept != nil {
			t.Errorf("swept %v with no buckets configured, want nothing", rec.swept)
		}
	})
}

func TestPurgeProjectValues(t *testing.T) {
	t.Run("empties the project's partition and reports the step", func(t *testing.T) {
		values := &valueRecorder{}
		var steps []string
		cfg := Config{Values: values}

		if err := purgeProjectValues(context.Background(), cfg, "shop", func(m string) { steps = append(steps, m) }); err != nil {
			t.Fatalf("purgeProjectValues: %v", err)
		}

		if want := []string{"shop"}; !reflect.DeepEqual(values.purged, want) {
			t.Errorf("purged = %v, want %v", values.purged, want)
		}
		if len(steps) != 1 {
			t.Errorf("reported steps = %v, want the removal reported as one step", steps)
		}
	})

	t.Run("no store is nothing to remove", func(t *testing.T) {
		var steps []string

		if err := purgeProjectValues(context.Background(), Config{}, "shop", func(m string) { steps = append(steps, m) }); err != nil {
			t.Fatalf("purgeProjectValues: %v", err)
		}
		if steps != nil {
			t.Errorf("reported %v with no store configured, want nothing", steps)
		}
	})

	t.Run("reports a failed removal", func(t *testing.T) {
		values := &valueRecorder{err: errors.New("table is on fire")}

		err := purgeProjectValues(context.Background(), Config{Values: values}, "shop", nil)
		if err == nil {
			t.Fatal("purgeProjectValues err = nil, want the failure reported")
		}
		if !strings.Contains(err.Error(), "table is on fire") {
			t.Errorf("err = %v, want it to carry what the store said", err)
		}
	})
}

func TestDestroyProject(t *testing.T) {
	t.Run("a failed value removal does not stop the steps after it", func(t *testing.T) {
		values := &valueRecorder{err: errors.New("table is on fire")}
		awsSide, cacheSide := &sweepRecorder{}, &sweepRecorder{}
		cfg := purgeConfig("prod", awsSide, cacheSide)
		cfg.Values = values
		index := &fakeStackIndex{projects: []string{"shop"}}
		cfg.Stacks = index

		_, err := DestroyProject(context.Background(), nil, cfg, "shop", ProjectTeardownStages{}, nil)

		if err == nil || !strings.Contains(err.Error(), "table is on fire") {
			t.Fatalf("DestroyProject err = %v, want the failed value removal reported", err)
		}
		if values.purged == nil {
			t.Fatal("the teardown never reached the value store, so nothing was stepped over")
		}
		want := []string{"asset-bucket|prod/shop/", "artifact-bucket|prod/shop/"}
		if !reflect.DeepEqual(awsSide.swept, want) {
			t.Errorf("account-side sweeps = %v, want %v — the steps after a failed value removal must still run", awsSide.swept, want)
		}
		if cacheSide.swept == nil {
			t.Error("the cache store was never swept, want the asset purge to have run anyway")
		}
		if index.projectsGone != nil {
			t.Errorf("index projects dropped = %v, want the project kept: the rerun that finishes the value removal reads it", index.projectsGone)
		}
	})
}

func TestRemovePreviewPurge(t *testing.T) {
	t.Run("never purges the project's artifacts", func(t *testing.T) {
		rec := &sweepRecorder{}
		fake := &recordingEdge{kind: edge.KindCloudflare}
		ctx := context.Background()
		state := fake.reconciled(t, edge.StackSpec{Version: "v1", Slug: "shop"})
		cfg := Config{ArtifactBucket: "artifact-bucket", Uploader: rec}

		if err := RemovePreview(ctx, state, cfg, "shop", "pr-1", false, PreviewRemovalStages{}, nil); err != nil {
			t.Fatalf("RemovePreview: %v", err)
		}

		if rec.swept != nil {
			t.Errorf("swept %v: the project prefix is shared by every pointer, live or landing", rec.swept)
		}
	})

	t.Run("leaves artifacts when the pointer removal failed", func(t *testing.T) {
		rec := &sweepRecorder{}
		fake := &recordingEdge{kind: edge.KindCloudflare}
		cfg := Config{ArtifactBucket: "artifact-bucket", Uploader: rec}
		stale := fake.opened(t, edge.StackState{edge.StackKeySlug: "shop", edge.StackKeySecret: "stale"})

		err := RemovePreview(context.Background(), stale, cfg, "shop", "pr-1", false, PreviewRemovalStages{}, nil)
		if err == nil {
			t.Fatal("RemovePreview err = nil, want the failed pointer removal reported")
		}
		if rec.swept != nil {
			t.Errorf("swept %v after a failed pointer removal, want nothing", rec.swept)
		}
	})

	t.Run("keeps the environment's overrides", func(t *testing.T) {
		values := &valueRecorder{}
		fake := &recordingEdge{kind: edge.KindCloudflare}
		ctx := context.Background()
		state := fake.reconciled(t, edge.StackSpec{Version: "v1", Slug: "shop"})

		if err := RemovePreview(ctx, state, Config{Values: values}, "shop", "pr-1", false, PreviewRemovalStages{}, nil); err != nil {
			t.Fatalf("RemovePreview: %v", err)
		}

		if values.purged != nil {
			t.Errorf("removing preview %q emptied %v: a redeployed branch would lose the override set for it", "pr-1", values.purged)
		}
	})
}
