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

func (tierBuckets) Buckets(_ context.Context, tier environment.Tier) (ports.Buckets, error) {
	return ports.Buckets{
		Functions: "ocel-artifacts-" + string(tier),
		Assets:    "ocel-assets-" + string(tier),
	}, nil
}

func everyStoreRef() []provider.ArtifactRef {
	return []provider.ArtifactRef{
		{Tier: environment.TierProduction, Bucket: provider.StoreFunctions, Key: "shop/prod/web/bundle.zip"},
		{Tier: environment.TierProduction, Bucket: provider.StoreAssets, Key: "shop/prod/web/static/app.js"},
	}
}

func TestArtifactsRunTheConformancePortTier(t *testing.T) {
	conformance.RunArtifactStore(t, provider.Facts{StoresArtifacts: true}, artifacts())
}

func TestAStoreThisProviderKeepsNoneOfRefusesRatherThanWritingNowhere(t *testing.T) {
	t.Parallel()

	cache := provider.ArtifactRef{Tier: environment.TierProduction, Bucket: provider.StoreCache, Key: "shop/prod/web/cache.json"}
	if err := artifacts().Put(context.Background(), cache, bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("Put() into a store this provider keeps none of succeeded, so the artifact went nowhere")
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
	var progress fake.Log
	if err := store.RemovePrefix(ctx, environment.TierProduction, "shop/prod/", &progress); err != nil {
		t.Fatalf("RemovePrefix() = %v", err)
	}
	if want := []string{"INFO Removed the production artifacts under shop/prod/ from 2 buckets"}; !slices.Equal(progress.Lines(), want) {
		t.Errorf("RemovePrefix() said %q, want %q", progress.Lines(), want)
	}
	for _, ref := range everyStoreRef() {
		body, err := store.Open(ctx, ref)
		if err == nil {
			body.Close()
			t.Errorf("%s survived the sweep, and a reclaimed release must leave nothing behind", ref.Bucket)
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
	if err := store.RemovePrefix(context.Background(), environment.TierProduction, "shop/prod/", nil); err != nil {
		t.Fatalf("RemovePrefix() over buckets already gone = %v, want the destroy to continue", err)
	}
}
