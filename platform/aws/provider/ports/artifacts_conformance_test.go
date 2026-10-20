package ports_test

import (
	"bytes"
	"context"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/platform/aws/provider/ports"
)

type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeS3() *fakeS3 { return &fakeS3{objects: map[string][]byte{}} }

func (f *fakeS3) at(bucket, key string) string { return bucket + "/" + key }

func (f *fakeS3) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Sorted(maps.Keys(f.objects))
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	blob, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[f.at(aws.ToString(in.Bucket), aws.ToString(in.Key))] = blob
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	blob, stored := f.objects[f.at(aws.ToString(in.Bucket), aws.ToString(in.Key))]
	if !stored {
		return nil, &s3types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(slices.Clone(blob)))}, nil
}

func (f *fakeS3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, stored := f.objects[f.at(aws.ToString(in.Bucket), aws.ToString(in.Key))]; !stored {
		return nil, &s3types.NotFound{}
	}
	return &s3.HeadObjectOutput{}, nil
}

func (f *fakeS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	bucket, prefix := aws.ToString(in.Bucket), aws.ToString(in.Prefix)
	var contents []s3types.Object
	for _, at := range slices.Sorted(maps.Keys(f.objects)) {
		key, under := strings.CutPrefix(at, bucket+"/")
		if under && strings.HasPrefix(key, prefix) {
			contents = append(contents, s3types.Object{Key: aws.String(key)})
		}
	}
	return &s3.ListObjectsV2Output{Contents: contents}, nil
}

func (f *fakeS3) DeleteObjects(_ context.Context, in *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range in.Delete.Objects {
		delete(f.objects, f.at(aws.ToString(in.Bucket), aws.ToString(id.Key)))
	}
	return &s3.DeleteObjectsOutput{}, nil
}

func artifacts() ports.Artifacts {
	return ports.Artifacts{S3: newFakeS3(), Stores: tierBuckets{}}
}

type tierBuckets struct{}

func (tierBuckets) Buckets(_ context.Context, tier environment.Tier) (naming.Buckets, error) {
	return naming.Buckets{
		naming.StorageArtifacts: "ocel-artifacts-" + string(tier),
		naming.StorageEdge:      "ocel-edge-" + string(tier),
		naming.StorageCache:     "ocel-cache-" + string(tier),
	}, nil
}

func everyBucketRef() []provider.ArtifactRef {
	release := releaseOf("r1a2b3c4d")
	release.Name = "api"
	return []provider.ArtifactRef{
		{Tier: environment.TierProduction, Key: release.FunctionArtifactKey("bundle")},
		{Tier: environment.TierProduction, Key: release.AssetKey("static/app.js")},
		{Tier: environment.TierProduction, Key: release.ObjectKey(naming.ObjectUseCache, "entry.json")},
	}
}

func TestArtifactsRunTheConformancePortTier(t *testing.T) {
	conformance.RunArtifactStore(t, provider.Facts{StoresArtifacts: true}, artifacts())
}

func TestAKeyOfNoKindOcelStoresIsRefusedRatherThanWrittenNowhere(t *testing.T) {
	t.Parallel()

	stray := provider.ArtifactRef{Tier: environment.TierProduction, Key: "prod/shop/web/r1a2b3c4d/cache.json"}
	if err := artifacts().Put(context.Background(), stray, bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("Put() of a key naming no kind succeeded, so the artifact went to a bucket nothing reads it from")
	}
}

func TestEachKindLandsInTheBucketOfItsDataClass(t *testing.T) {
	t.Parallel()

	release := releaseOf("r1a2b3c4d")
	release.Name = "api"
	for _, tc := range []struct {
		kind   naming.ObjectKind
		bucket string
	}{
		{naming.ObjectFunction, "ocel-artifacts-production"},
		{naming.ObjectBytecode, "ocel-artifacts-production"},
		{naming.ObjectAssets, "ocel-edge-production"},
		{naming.ObjectPages, "ocel-edge-production"},
		{naming.ObjectRouteTables, "ocel-edge-production"},
		{naming.ObjectBundles, "ocel-edge-production"},
		{naming.ObjectImageConfig, "ocel-edge-production"},
		{naming.ObjectFetchCache, "ocel-cache-production"},
		{naming.ObjectUseCache, "ocel-cache-production"},
		{naming.ObjectTagClock, "ocel-cache-production"},
	} {
		s3 := newFakeS3()
		store := ports.Artifacts{S3: s3, Stores: tierBuckets{}}
		ref := provider.ArtifactRef{Tier: environment.TierProduction, Key: release.ObjectKey(tc.kind, "object")}
		if err := store.Put(context.Background(), ref, bytes.NewReader([]byte("x"))); err != nil {
			t.Fatal(err)
		}
		if _, stored := s3.objects[tc.bucket+"/"+ref.Key]; !stored {
			t.Errorf("%s objects are stored at %v, want %s", tc.kind, s3.keys(), tc.bucket)
		}
	}
	s3 := newFakeS3()
	store := ports.Artifacts{S3: s3, Stores: tierBuckets{}}
	payload := provider.ArtifactRef{Tier: environment.TierProduction, Key: naming.PayloadKey("runtime", "abc")}
	if err := store.Put(context.Background(), payload, bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	if _, stored := s3.objects["ocel-artifacts-production/"+payload.Key]; !stored {
		t.Errorf("payloads are stored at %v, want the artifacts bucket", s3.keys())
	}
}

func TestASweepReachesEveryBucketTheAccountKeeps(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := artifacts()
	for _, ref := range everyBucketRef() {
		if err := store.Put(ctx, ref, bytes.NewReader([]byte("x"))); err != nil {
			t.Fatal(err)
		}
	}
	var progress fake.Log
	scope, err := naming.AppScope(releaseOf("r1a2b3c4d"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveObjects(ctx, environment.TierProduction, scope, []naming.ObjectKind{naming.ObjectFunction, naming.ObjectAssets, naming.ObjectUseCache}, &progress); err != nil {
		t.Fatalf("RemoveObjects() = %v", err)
	}
	if want := []string{"INFO Removed the production objects under fn/shop/prod/web/, assets/shop/prod/web/, use-cache/shop/prod/web/"}; !slices.Equal(progress.Lines(), want) {
		t.Errorf("RemoveObjects() said %q, want %q", progress.Lines(), want)
	}
	for _, ref := range everyBucketRef() {
		body, err := store.Open(ctx, ref)
		if err == nil {
			body.Close()
			t.Errorf("%s survived the sweep, and a reclaimed release must leave nothing behind", ref.Key)
		}
	}
}

type goneS3 struct{ *fakeS3 }

func (goneS3) ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	return nil, &s3types.NoSuchBucket{}
}

func TestASweepOfABucketAlreadyTornDownIsNoWork(t *testing.T) {
	t.Parallel()

	store := ports.Artifacts{S3: goneS3{newFakeS3()}, Stores: tierBuckets{}}
	scope, err := naming.ProjectScope("shop")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveObjects(context.Background(), environment.TierProduction, scope, naming.CoordinateObjectKinds, nil); err != nil {
		t.Fatalf("RemoveObjects() over buckets already gone = %v, want the destroy to continue", err)
	}
}
