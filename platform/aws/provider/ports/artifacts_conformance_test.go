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

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	"github.com/ocelhq/ocel/platform/aws/provider/ports"
)

type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeS3() *fakeS3 { return &fakeS3{objects: map[string][]byte{}} }

func (f *fakeS3) at(bucket, key string) string { return bucket + "/" + key }

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
	blob, held := f.objects[f.at(aws.ToString(in.Bucket), aws.ToString(in.Key))]
	if !held {
		return nil, &s3types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(slices.Clone(blob)))}, nil
}

func (f *fakeS3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, held := f.objects[f.at(aws.ToString(in.Bucket), aws.ToString(in.Key))]; !held {
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
	return ports.Artifacts{S3: newFakeS3(), Stores: classBuckets{}}
}

type classBuckets struct {
	cacheless bool
	cache     ports.S3API
}

func (b classBuckets) Buckets(_ context.Context, class providerkit.Class) (ports.Buckets, error) {
	held := ports.Buckets{
		Functions: "ocel-artifacts-" + string(class),
		Assets:    "ocel-assets-" + string(class),
	}
	if !b.cacheless {
		held.Caches = []ports.CacheBucket{{Name: "ocel-cache-" + string(class), S3: b.cache}}
	}
	return held, nil
}

func providerkitCacheRef() providerkit.ArtifactRef {
	return providerkit.ArtifactRef{Class: providerkit.ClassProduction, Bucket: providerkit.StoreCache, Key: "shop/prod/web/cache.json"}
}

func everyStoreRef() []providerkit.ArtifactRef {
	return []providerkit.ArtifactRef{
		{Class: providerkit.ClassProduction, Bucket: providerkit.StoreFunctions, Key: "shop/prod/web/bundle.zip"},
		{Class: providerkit.ClassProduction, Bucket: providerkit.StoreAssets, Key: "shop/prod/web/static/app.js"},
		providerkitCacheRef(),
	}
}

func TestArtifactsRunTheKitsPortTier(t *testing.T) {
	conformance.RunArtifactStore(t, artifacts())
}

func TestAStoreThisAccountHasNoBucketForRefusesRatherThanWritingNowhere(t *testing.T) {
	t.Parallel()

	store := artifacts()
	store.Stores = classBuckets{cacheless: true}
	if err := store.Put(context.Background(), providerkitCacheRef(), bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("Put() into a store this account has no bucket for succeeded, so the artifact went nowhere")
	}
}

func TestAPrefixSweepReachesEveryStoreTheAccountKeeps(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := artifacts()
	for _, ref := range everyStoreRef() {
		if err := store.Put(ctx, ref, bytes.NewReader([]byte("x"))); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RemovePrefix(ctx, providerkit.ClassProduction, "shop/prod/", nil); err != nil {
		t.Fatalf("RemovePrefix() = %v", err)
	}
	for _, ref := range everyStoreRef() {
		body, err := store.Open(ctx, ref)
		if err == nil {
			body.Close()
			t.Errorf("%s survived the sweep, and a reclaimed release must leave nothing behind", ref.Bucket)
		}
	}
}

func TestACacheStoreOffTheAccountsEndpointIsSweptThroughItsOwnClient(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	elsewhere := newFakeS3()
	store := ports.Artifacts{S3: newFakeS3(), Stores: classBuckets{cache: elsewhere}}
	ref := providerkitCacheRef()
	if err := store.Put(ctx, ref, bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	if _, held := elsewhere.objects[elsewhere.at("ocel-cache-"+string(ref.Class), ref.Key)]; !held {
		t.Fatal("Put() wrote the cache artifact through the account's own client, not the store's")
	}
	if err := store.RemovePrefix(ctx, providerkit.ClassProduction, "shop/prod/", nil); err != nil {
		t.Fatalf("RemovePrefix() = %v", err)
	}
	if len(elsewhere.objects) != 0 {
		t.Fatalf("the cache store still holds %d object(s) after the sweep", len(elsewhere.objects))
	}
}

type goneS3 struct{ *fakeS3 }

func (goneS3) ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	return nil, &s3types.NoSuchBucket{}
}

func TestASweepOfACacheStoreAlreadyTornDownIsNoWork(t *testing.T) {
	t.Parallel()

	store := ports.Artifacts{S3: newFakeS3(), Stores: classBuckets{cache: goneS3{newFakeS3()}}}
	if err := store.RemovePrefix(context.Background(), providerkit.ClassProduction, "shop/prod/", nil); err != nil {
		t.Fatalf("RemovePrefix() over a cache bucket already gone = %v, want the destroy to carry on", err)
	}
}
