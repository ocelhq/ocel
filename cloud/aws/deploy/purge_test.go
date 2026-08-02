package deploy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/cloud/edge"
)

// sweepRecorder records the (bucket, prefix) pair of every list a purge issues
// and reports the prefix empty. What these tests pin down is which prefixes a
// teardown reaches — deletePrefix's own paging and error propagation are
// covered by TestDeletePrefix_*.
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

// purgeConfig is the teardown Config shape both purges read, with one recorder
// per substrate so a test can tell an artifact/asset sweep from a cache-store
// one by which recorder saw it.
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

func TestPurgeProjectAssets_SweepsTheArtifactPrefixAlongsideTheRest(t *testing.T) {
	awsSide, cacheSide := &sweepRecorder{}, &sweepRecorder{}

	if err := purgeProjectAssets(context.Background(), purgeConfig("prod", awsSide, cacheSide), "shop"); err != nil {
		t.Fatalf("purgeProjectAssets: %v", err)
	}

	wantAWS := []string{
		"asset-bucket|prod/shop/",
		"artifact-bucket|shop/",
	}
	if !reflect.DeepEqual(awsSide.swept, wantAWS) {
		t.Errorf("account-side sweeps = %v, want %v", awsSide.swept, wantAWS)
	}
	wantCache := []string{
		"cache-bucket|assets/shop/",
		"cache-bucket|edge/shop/",
		"cache-bucket|prod/shop/",
	}
	if !reflect.DeepEqual(cacheSide.swept, wantCache) {
		t.Errorf("cache-store sweeps = %v, want %v", cacheSide.swept, wantCache)
	}
}

func TestPurgePreviewAssets_SweepsTheArtifactPrefixAndEveryPointersISR(t *testing.T) {
	awsSide, cacheSide := &sweepRecorder{}, &sweepRecorder{}

	err := purgePreviewAssets(context.Background(), purgeConfig("preview-pr-1", awsSide, cacheSide), "shop", []string{"pr-1", "staging"})
	if err != nil {
		t.Fatalf("purgePreviewAssets: %v", err)
	}

	wantAWS := []string{
		"artifact-bucket|shop/",
		"asset-bucket|preview-pr-1/shop/",
		"asset-bucket|preview-staging/shop/",
	}
	if !reflect.DeepEqual(awsSide.swept, wantAWS) {
		t.Errorf("account-side sweeps = %v, want %v", awsSide.swept, wantAWS)
	}
	wantCache := []string{
		"cache-bucket|assets/shop/",
		"cache-bucket|edge/shop/",
		"cache-bucket|preview-pr-1/shop/",
		"cache-bucket|preview-staging/shop/",
	}
	if !reflect.DeepEqual(cacheSide.swept, wantCache) {
		t.Errorf("cache-store sweeps = %v, want %v", cacheSide.swept, wantCache)
	}
}

func TestPurgeProjectArtifacts_MissingBucketIsANoOp(t *testing.T) {
	rec := &sweepRecorder{}
	cfg := Config{Uploader: rec}

	if err := purgeProjectArtifacts(context.Background(), cfg, "shop"); err != nil {
		t.Fatalf("purgeProjectArtifacts: %v", err)
	}
	if rec.swept != nil {
		t.Errorf("swept %v with no artifact bucket configured, want nothing", rec.swept)
	}
}

// The artifact key carries no pointer, so identical code under two pointers is
// one object: a sibling still live means the prefix must stay.
func TestRemovePreview_LeavesArtifactsWhileSiblingPointersRemain(t *testing.T) {
	rec := &sweepRecorder{}
	fake := &recordingRootStack{
		pointerRemoval: edge.PointerRemoval{
			RemainingPointers: 1,
			RemovedRoutes:     []edge.RemovedRoute{{App: "web", Hostname: "pr-1-aaaaaaaaaa.preview.acme.com"}},
		},
	}
	ctx := context.Background()
	state, err := fake.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1", Slug: "shop"}, nil)
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}
	cfg := Config{ArtifactBucket: "artifact-bucket", Uploader: rec}

	if err := RemovePreview(ctx, fake, state, cfg, "shop", "pr-1", false, nil, nil); err != nil {
		t.Fatalf("RemovePreview: %v", err)
	}

	if rec.swept != nil {
		t.Errorf("swept %v: a live sibling pointer may still run this code", rec.swept)
	}
}

func TestRemovePreview_PurgesArtifactsWhenItWasTheLastPointer(t *testing.T) {
	rec := &sweepRecorder{}
	fake := &recordingRootStack{
		pointerRemoval: edge.PointerRemoval{
			RemainingPointers: 0,
			RemovedRoutes:     []edge.RemovedRoute{{App: "web", Hostname: "pr-1-aaaaaaaaaa.preview.acme.com"}},
		},
	}
	ctx := context.Background()
	state, err := fake.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1", Slug: "shop"}, nil)
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}
	cfg := Config{ArtifactBucket: "artifact-bucket", Uploader: rec}

	if err := RemovePreview(ctx, fake, state, cfg, "shop", "pr-1", false, nil, nil); err != nil {
		t.Fatalf("RemovePreview: %v", err)
	}

	if want := []string{"artifact-bucket|shop/"}; !reflect.DeepEqual(rec.swept, want) {
		t.Errorf("swept = %v, want %v", rec.swept, want)
	}
}

// RemainingPointers is zero both when the pointer was the last one and when
// RemovePointer failed outright — purging on the latter would take every
// sibling pointer's artifacts with it.
func TestRemovePreview_LeavesArtifactsWhenThePointerRemovalFailed(t *testing.T) {
	rec := &sweepRecorder{}
	fake := &recordingRootStack{}
	cfg := Config{ArtifactBucket: "artifact-bucket", Uploader: rec}
	state := edge.RootStackState{edge.RootStackKeySlug: "shop", edge.RootStackKeySecret: "stale"}

	err := RemovePreview(context.Background(), fake, state, cfg, "shop", "pr-1", false, nil, nil)
	if err == nil {
		t.Fatal("RemovePreview err = nil, want the failed pointer removal reported")
	}
	if rec.swept != nil {
		t.Errorf("swept %v after a failed pointer removal, want nothing", rec.swept)
	}
}

// valueRecorder stands in for the substrate's variable store, recording which
// projects a teardown emptied and failing on demand.
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

func TestPurgeProjectValues_EmptiesTheProjectsPartitionAndReportsTheStep(t *testing.T) {
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
}

// A bootstrap predating the variable store leaves nothing to remove, which is
// not a failure and not a step worth reporting.
func TestPurgeProjectValues_NoStoreIsNothingToRemove(t *testing.T) {
	var steps []string

	if err := purgeProjectValues(context.Background(), Config{}, "shop", func(m string) { steps = append(steps, m) }); err != nil {
		t.Fatalf("purgeProjectValues: %v", err)
	}
	if steps != nil {
		t.Errorf("reported %v with no store configured, want nothing", steps)
	}
}

func TestPurgeProjectValues_ReportsAFailedRemoval(t *testing.T) {
	values := &valueRecorder{err: errors.New("table is on fire")}

	err := purgeProjectValues(context.Background(), Config{Values: values}, "shop", nil)
	if err == nil {
		t.Fatal("purgeProjectValues err = nil, want the failure reported")
	}
	if !strings.Contains(err.Error(), "table is on fire") {
		t.Errorf("err = %v, want it to carry what the store said", err)
	}
}

// Removing one preview removes compute, not values: the override someone set
// for that environment is what a redeploy of the same branch resolves, and
// overrides are tiny.
func TestRemovePreview_KeepsTheEnvironmentsOverrides(t *testing.T) {
	values := &valueRecorder{}
	fake := &recordingRootStack{
		pointerRemoval: edge.PointerRemoval{
			RemainingPointers: 0,
			RemovedRoutes:     []edge.RemovedRoute{{App: "web", Hostname: "pr-1-aaaaaaaaaa.preview.acme.com"}},
		},
	}
	ctx := context.Background()
	state, err := fake.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1", Slug: "shop"}, nil)
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}

	if err := RemovePreview(ctx, fake, state, Config{Values: values}, "shop", "pr-1", false, nil, nil); err != nil {
		t.Fatalf("RemovePreview: %v", err)
	}

	if values.purged != nil {
		t.Errorf("removing preview %q emptied %v: a redeployed branch would lose the override set for it", "pr-1", values.purged)
	}
}
