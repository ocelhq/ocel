package ports_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/ports"
)

type retiringWriter struct {
	mu      sync.Mutex
	retired []string
	order   *[]string
	fail    error
}

func (w *retiringWriter) Destroy(_ context.Context, isrPrefix string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.retired = append(w.retired, isrPrefix)
	if w.order != nil {
		*w.order = append(*w.order, "retire "+isrPrefix)
	}
	return w.fail
}

type orderedS3 struct {
	*fakeS3
	order *[]string
}

func (o orderedS3) DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, fns ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	*o.order = append(*o.order, "sweep")
	return o.fakeS3.DeleteObjects(ctx, in, fns...)
}

type writerStores struct {
	cache  ports.S3API
	writer ports.ISRWriter
}

func (s writerStores) Buckets(_ context.Context, tier environment.Tier) (ports.Buckets, error) {
	return ports.Buckets{
		Functions: "ocel-artifacts-" + string(tier),
		Assets:    "ocel-assets-" + string(tier),
		Caches:    []ports.CacheBucket{{Name: "ocel-cache-" + string(tier), S3: s.cache, Writer: s.writer}},
	}, nil
}

func cacheEntryUnder(prefix string) provider.ArtifactRef {
	return provider.ArtifactRef{Tier: environment.TierProduction, Bucket: provider.StoreCache, Key: prefix + "cache/blog.cache.json"}
}

const release = "production/shop/web/r1a2b3c4d/"

func TestPruningAReleaseBehindCloudflareDestroysItsISRWriterRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	writer := &retiringWriter{}
	cache := newFakeS3()
	store := ports.Artifacts{S3: newFakeS3(), Stores: writerStores{cache: cache, writer: writer}}
	if err := store.Put(ctx, cacheEntryUnder(release+"isr/"), bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}

	for _, prefix := range []string{release, release + "isr/"} {
		writer.retired = nil
		if err := store.RemovePrefix(ctx, environment.TierProduction, prefix, nil); err != nil {
			t.Fatalf("RemovePrefix(%s) = %v", prefix, err)
		}
		if len(writer.retired) != 1 || writer.retired[0] != "production/shop/web/r1a2b3c4d/isr" {
			t.Errorf("RemovePrefix(%s) retired %v, want the release's isr prefix once", prefix, writer.retired)
		}
	}
}

func TestPruningAReleaseRetiresTheWriterBeforeSweeping(t *testing.T) {
	t.Parallel()
	var order []string
	writer := &retiringWriter{order: &order}
	store := ports.Artifacts{
		S3:     newFakeS3(),
		Stores: writerStores{cache: orderedS3{fakeS3: newFakeS3(), order: &order}, writer: writer},
	}
	if err := store.Put(context.Background(), cacheEntryUnder(release+"isr/"), bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}

	if err := store.RemovePrefix(context.Background(), environment.TierProduction, release, nil); err != nil {
		t.Fatal(err)
	}

	if len(order) == 0 || order[0] != "retire production/shop/web/r1a2b3c4d/isr" {
		t.Errorf("order = %v, want the writer's record retired before the sweep so nothing re-writes what the sweep deletes", order)
	}
}

func TestRemovingAProjectPrefixDestroysNoISRWriterRecord(t *testing.T) {
	t.Parallel()
	writer := &retiringWriter{}
	store := ports.Artifacts{S3: newFakeS3(), Stores: writerStores{cache: newFakeS3(), writer: writer}}

	if err := store.RemovePrefix(context.Background(), environment.TierProduction, "production/shop/", nil); err != nil {
		t.Fatal(err)
	}
	if len(writer.retired) != 0 {
		t.Errorf("retired %v for a prefix that names no release", writer.retired)
	}
}

func TestAWriterThatFailsToRetireFailsTheRemovalAfterTheSweep(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	writer := &retiringWriter{fail: errors.New("the writer is gone")}
	cache := newFakeS3()
	store := ports.Artifacts{S3: newFakeS3(), Stores: writerStores{cache: cache, writer: writer}}
	ref := cacheEntryUnder(release + "isr/")
	if err := store.Put(ctx, ref, bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}

	err := store.RemovePrefix(ctx, environment.TierProduction, release, nil)

	if err == nil || !strings.Contains(err.Error(), "retire the isr writer's record of production/shop/web/r1a2b3c4d/isr") {
		t.Fatalf("RemovePrefix() = %v, want the failed retirement named", err)
	}
	if _, kept := cache.objects[cache.at("ocel-cache-production", ref.Key)]; kept {
		t.Error("the sweep did not run after the writer failed: a retired record is not a reason to leave the objects")
	}
}
