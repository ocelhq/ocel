package conformance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func RunArtifactStore(t *testing.T, facts provider.Facts, artifacts provider.ArtifactStore) {
	t.Helper()

	ctx := context.Background()
	scope := conformanceRelease(t.Name())
	ref := provider.ArtifactRef{Tier: environment.TierProduction, Key: scope.AssetKey("bundle")}
	body := []byte("a build artifact")

	if !facts.StoresArtifacts {
		runStorelessArtifactStore(t, artifacts, ref)
		return
	}

	t.Run("what Put stores, Open reads back", func(t *testing.T) {
		if err := artifacts.Put(ctx, ref, bytes.NewReader(body)); err != nil {
			t.Fatalf("Put() = %v, want the artifact stored", err)
		}
		opened, err := artifacts.Open(ctx, ref)
		if err != nil {
			t.Fatalf("Open() of an artifact just put = %v", err)
		}
		defer opened.Close()
		read, err := io.ReadAll(opened)
		if err != nil || !bytes.Equal(read, body) {
			t.Fatalf("Open() read %q, %v, want %q", read, err, body)
		}
	})

	t.Run("Has answers for a key stored and for one nothing wrote", func(t *testing.T) {
		present, err := artifacts.Has(ctx, ref)
		if err != nil || !present {
			t.Errorf("Has() of an artifact just put = %v, %v, want true: a deploy re-uploads every unchanged build without it", present, err)
		}
		absent := provider.ArtifactRef{Tier: ref.Tier, Key: ref.Key + ".never-written"}
		present, err = artifacts.Has(ctx, absent)
		if err != nil {
			t.Errorf("Has() of a key nothing wrote = %v, want a plain false", err)
		}
		if present {
			t.Error("Has() claims a key nothing wrote is stored, so a changed build would never be uploaded")
		}
	})

	t.Run("Open of a key nothing wrote refuses rather than answering empty", func(t *testing.T) {
		absent := provider.ArtifactRef{Tier: ref.Tier, Key: ref.Key + ".never-written"}
		opened, err := artifacts.Open(ctx, absent)
		if err == nil {
			opened.Close()
			t.Fatal("Open() of a key nothing wrote succeeded, so a missing artifact reads as an empty one")
		}
	})

	t.Run("RemoveObjects takes the kinds under the scope and nothing beside them", func(t *testing.T) {
		removed := conformanceRelease(t.Name())
		gone := provider.ArtifactRef{Tier: ref.Tier, Key: removed.AssetKey("bundle")}
		sibling := removed
		sibling.Release = naming.NewReleaseToken(t.Name()+"-sibling", "")
		kept := provider.ArtifactRef{Tier: ref.Tier, Key: sibling.AssetKey("bundle")}
		for _, at := range []provider.ArtifactRef{gone, kept} {
			if err := artifacts.Put(ctx, at, bytes.NewReader(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := artifacts.RemoveObjects(ctx, ref.Tier, releaseScope(t, removed), naming.CoordinateObjectKinds, nil); err != nil {
			t.Fatalf("RemoveObjects() = %v", err)
		}
		if present, err := artifacts.Has(ctx, gone); err != nil || present {
			t.Errorf("Has() of an artifact in the scope removed = %v, %v, want false", present, err)
		}
		opened, err := artifacts.Open(ctx, kept)
		if err != nil {
			t.Fatalf("Open() of an artifact outside the scope removed = %v, want it still stored", err)
		}
		opened.Close()
	})

	t.Run("RemoveObjects of one tier leaves the other tier's artifacts", func(t *testing.T) {
		removed := conformanceRelease(t.Name())
		key := removed.AssetKey("bundle")
		production := provider.ArtifactRef{Tier: environment.TierProduction, Key: key}
		preview := provider.ArtifactRef{Tier: environment.TierPreview, Key: key}
		for _, at := range []provider.ArtifactRef{production, preview} {
			if err := artifacts.Put(ctx, at, bytes.NewReader(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := artifacts.RemoveObjects(ctx, environment.TierPreview, releaseScope(t, removed), []naming.ObjectKind{naming.ObjectAssets}, nil); err != nil {
			t.Fatalf("RemoveObjects() = %v", err)
		}
		opened, err := artifacts.Open(ctx, production)
		if err != nil {
			t.Fatalf("Open() of the production artifact after a preview scope was removed = %v, want it still stored", err)
		}
		opened.Close()
	})

	t.Run("RemoveObjects of a scope containing nothing is not an error", func(t *testing.T) {
		if err := artifacts.RemoveObjects(ctx, ref.Tier, releaseScope(t, conformanceRelease(t.Name())), naming.CoordinateObjectKinds, nil); err != nil {
			t.Fatalf("RemoveObjects() of a scope nothing was written under = %v, want nil", err)
		}
	})

	t.Run("RemoveObjects of an app's scope takes its every release and leaves its neighbours", func(t *testing.T) {
		removed := conformanceRelease(t.Name())
		removed.Project = "conformance-app"
		other := removed
		other.Release = naming.NewReleaseToken(t.Name()+"-other", "")
		neighbour := removed
		neighbour.App = "admin"
		gone := []provider.ArtifactRef{{Tier: ref.Tier, Key: removed.AssetKey("bundle")}, {Tier: ref.Tier, Key: other.AssetKey("bundle")}}
		kept := provider.ArtifactRef{Tier: ref.Tier, Key: neighbour.AssetKey("bundle")}
		for _, at := range append(gone, kept) {
			if err := artifacts.Put(ctx, at, bytes.NewReader(body)); err != nil {
				t.Fatal(err)
			}
		}
		scope, err := naming.AppScope(removed)
		if err != nil {
			t.Fatal(err)
		}
		if err := artifacts.RemoveObjects(ctx, ref.Tier, scope, []naming.ObjectKind{naming.ObjectAssets}, nil); err != nil {
			t.Fatalf("RemoveObjects() = %v", err)
		}
		for _, at := range gone {
			if present, err := artifacts.Has(ctx, at); err != nil || present {
				t.Errorf("Has(%s) of a release of the app removed = %v, %v, want false", at.Key, present, err)
			}
		}
		if present, err := artifacts.Has(ctx, kept); err != nil || !present {
			t.Errorf("Has(%s) of a neighbouring app = %v, %v, want it still stored", kept.Key, present, err)
		}
	})

	t.Run("RemoveObjects of a scope naming no project is refused", func(t *testing.T) {
		var refused refusal.Refusal
		if err := artifacts.RemoveObjects(ctx, ref.Tier, naming.StorageScope{}, naming.CoordinateObjectKinds, nil); !errors.As(err, &refused) {
			t.Fatalf("RemoveObjects() of an empty scope = %v, want a refusal: it names every project's objects", err)
		}
	})
}

func conformanceRelease(name string) naming.Coordinate {
	return naming.Coordinate{Project: "conformance", Env: "prod", App: "web", Name: "api", Release: naming.NewReleaseToken(name, "")}
}

func releaseScope(t *testing.T, release naming.Coordinate) naming.StorageScope {
	t.Helper()
	scope, err := naming.ReleaseScope(release)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func runStorelessArtifactStore(t *testing.T, artifacts provider.ArtifactStore, ref provider.ArtifactRef) {
	t.Helper()

	ctx := context.Background()

	t.Run("Put refuses rather than accepting a write nothing will ever read back", func(t *testing.T) {
		var refused refusal.Refusal
		err := artifacts.Put(ctx, ref, bytes.NewReader([]byte("a build artifact")))
		if !errors.As(err, &refused) {
			t.Fatalf("Put() into a provider that keeps no artifacts = %v, want a refusal: a store that reports a write it loses is worse than one that has none", err)
		}
		if refused.Code != refusal.CodeInvalid {
			t.Errorf("Put() refused with %q, want %q so the CLI renders it as the caller's mistake", refused.Code, refusal.CodeInvalid)
		}
		if refused.Message == "" {
			t.Error("Put() refused with no message, so the CLI has nothing to tell the user")
		}
	})

	t.Run("Open refuses rather than answering empty", func(t *testing.T) {
		var refused refusal.Refusal
		opened, err := artifacts.Open(ctx, ref)
		if !errors.As(err, &refused) {
			if err == nil {
				opened.Close()
			}
			t.Fatalf("Open() from a provider that keeps no artifacts = %v, want a refusal: a missing artifact must never read as an empty one", err)
		}
	})

	t.Run("Has answers a plain false", func(t *testing.T) {
		present, err := artifacts.Has(ctx, ref)
		if err != nil {
			t.Fatalf("Has() = %v, want a plain false: plan synthesis draws its create row from this answer", err)
		}
		if present {
			t.Error("Has() claims a provider that keeps no artifacts has one")
		}
	})

	t.Run("RemoveObjects of any scope, including one nothing wrote under, is nil", func(t *testing.T) {
		for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
			project, err := naming.ProjectScope("conformance")
			if err != nil {
				t.Fatal(err)
			}
			for _, scope := range []naming.StorageScope{project, releaseScope(t, conformanceRelease(t.Name()))} {
				if err := artifacts.RemoveObjects(ctx, tier, scope, naming.CoordinateObjectKinds, nil); err != nil {
					t.Errorf("RemoveObjects(%s, %+v) = %v, want nil: teardown sweeps it on every destroy and every preview reap", tier, scope, err)
				}
			}
		}
	})
}
