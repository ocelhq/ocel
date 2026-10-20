package ports_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/ports"
)

func releaseOf(token string) naming.Coordinate {
	release, err := naming.ParseReleaseToken(token)
	if err != nil {
		panic(err)
	}
	return naming.Coordinate{Project: "shop", Env: "prod", App: "web", Release: release}
}

func pageOf(release naming.Coordinate) provider.ArtifactRef {
	return provider.ArtifactRef{Tier: environment.TierProduction, Key: release.ObjectKey(naming.ObjectPages, "blog.cache.json")}
}

func TestPruningAReleaseRemovesItsPagesFromTheEdgeBucket(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := ports.Artifacts{S3: newFakeS3(), Stores: tierBuckets{}}
	pruned := releaseOf("r1a2b3c4d")
	entry, other := pageOf(pruned), pageOf(releaseOf("r5e6f7a8b"))
	for _, ref := range []provider.ArtifactRef{entry, other} {
		if err := store.Put(ctx, ref, bytes.NewReader([]byte("x"))); err != nil {
			t.Fatal(err)
		}
	}

	scope, err := naming.ReleaseScope(pruned)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveObjects(ctx, environment.TierProduction, scope, naming.ISRObjectKinds, nil); err != nil {
		t.Fatalf("RemoveObjects(%+v) = %v", pruned, err)
	}

	if has, err := store.Has(ctx, entry); err != nil || has {
		t.Errorf("Has(%s) = %v, %v after the release was pruned, want its pages gone", entry.Key, has, err)
	}
	if has, err := store.Has(ctx, other); err != nil || !has {
		t.Errorf("Has(%s) = %v, %v, want another release's pages kept", other.Key, has, err)
	}
}
