package ports_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const release = "production/shop/web/r1a2b3c4d/"

func isrEntryUnder(prefix string) provider.ArtifactRef {
	return provider.ArtifactRef{Tier: environment.TierProduction, Bucket: provider.StoreAssets, Key: prefix + "cache/blog.cache.json"}
}

func TestPruningAReleaseRemovesItsISREntriesFromTheAssetBucket(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := ports.Artifacts{S3: newFakeS3(), Stores: tierBuckets{}}
	entry := isrEntryUnder(release + "isr/")
	other := isrEntryUnder("production/shop/web/r5e6f7a8b/isr/")
	for _, ref := range []provider.ArtifactRef{entry, other} {
		if err := store.Put(ctx, ref, bytes.NewReader([]byte("x"))); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.RemovePrefix(ctx, environment.TierProduction, release, nil); err != nil {
		t.Fatalf("RemovePrefix(%s) = %v", release, err)
	}

	if has, err := store.Has(ctx, entry); err != nil || has {
		t.Errorf("Has(%s) = %v, %v after the release was pruned, want its ISR entries gone", entry.Key, has, err)
	}
	if has, err := store.Has(ctx, other); err != nil || !has {
		t.Errorf("Has(%s) = %v, %v, want another release's ISR entries kept", other.Key, has, err)
	}
}

func TestPruningTheISRPrefixAloneRemovesItsEntries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := ports.Artifacts{S3: newFakeS3(), Stores: tierBuckets{}}
	entry := isrEntryUnder(release + "isr/")
	if err := store.Put(ctx, entry, bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}

	if err := store.RemovePrefix(ctx, environment.TierProduction, release+"isr/", nil); err != nil {
		t.Fatal(err)
	}

	if has, err := store.Has(ctx, entry); err != nil || has {
		t.Errorf("Has(%s) = %v, %v, want the entry gone", entry.Key, has, err)
	}
}
