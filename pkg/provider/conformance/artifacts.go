package conformance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func RunArtifactStore(t *testing.T, facts provider.Facts, artifacts provider.ArtifactStore) {
	t.Helper()

	ctx := context.Background()
	ref := provider.ArtifactRef{Tier: environment.TierProduction, Bucket: provider.StoreFunctions, Key: "conformance/" + t.Name() + "/bundle.zip"}
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
		absent := provider.ArtifactRef{Tier: ref.Tier, Bucket: ref.Bucket, Key: ref.Key + ".never-written"}
		present, err = artifacts.Has(ctx, absent)
		if err != nil {
			t.Errorf("Has() of a key nothing wrote = %v, want a plain false", err)
		}
		if present {
			t.Error("Has() claims a key nothing wrote is stored, so a changed build would never be uploaded")
		}
	})

	t.Run("Open of a key nothing wrote refuses rather than answering empty", func(t *testing.T) {
		absent := provider.ArtifactRef{Tier: ref.Tier, Bucket: ref.Bucket, Key: ref.Key + ".never-written"}
		opened, err := artifacts.Open(ctx, absent)
		if err == nil {
			opened.Close()
			t.Fatal("Open() of a key nothing wrote succeeded, so a missing artifact reads as an empty one")
		}
	})

	t.Run("RemovePrefix takes the prefix and nothing beside it", func(t *testing.T) {
		kept := provider.ArtifactRef{Tier: ref.Tier, Bucket: ref.Bucket, Key: "conformance/" + t.Name() + "-sibling/bundle.zip"}
		if err := artifacts.Put(ctx, kept, bytes.NewReader(body)); err != nil {
			t.Fatal(err)
		}
		if err := artifacts.RemovePrefix(ctx, ref.Tier, "conformance/"+t.Name()+"/", nil); err != nil {
			t.Fatalf("RemovePrefix() = %v", err)
		}
		opened, err := artifacts.Open(ctx, kept)
		if err != nil {
			t.Fatalf("Open() of a key outside the prefix removed = %v, want it still stored", err)
		}
		opened.Close()
	})

	t.Run("RemovePrefix of one tier leaves the other tier's artifacts", func(t *testing.T) {
		key := "conformance/" + t.Name() + "/bundle.zip"
		production := provider.ArtifactRef{Tier: environment.TierProduction, Bucket: ref.Bucket, Key: key}
		preview := provider.ArtifactRef{Tier: environment.TierPreview, Bucket: ref.Bucket, Key: key}
		for _, at := range []provider.ArtifactRef{production, preview} {
			if err := artifacts.Put(ctx, at, bytes.NewReader(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := artifacts.RemovePrefix(ctx, environment.TierPreview, "conformance/"+t.Name()+"/", nil); err != nil {
			t.Fatalf("RemovePrefix() = %v", err)
		}
		opened, err := artifacts.Open(ctx, production)
		if err != nil {
			t.Fatalf("Open() of the production artifact after a preview prefix was removed = %v, want it still stored", err)
		}
		opened.Close()
	})

	t.Run("RemovePrefix of a prefix containing nothing is not an error", func(t *testing.T) {
		if err := artifacts.RemovePrefix(ctx, ref.Tier, "conformance/"+t.Name()+"/nothing-here/", nil); err != nil {
			t.Fatalf("RemovePrefix() of a prefix nothing was written under = %v, want nil", err)
		}
	})
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

	t.Run("RemovePrefix of any prefix, including one nothing wrote under, is nil", func(t *testing.T) {
		for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
			for _, prefix := range []string{"conformance/" + t.Name() + "/", "conformance/" + t.Name() + "/nothing-here/"} {
				if err := artifacts.RemovePrefix(ctx, tier, prefix, nil); err != nil {
					t.Errorf("RemovePrefix(%s, %q) = %v, want nil: teardown sweeps it on every destroy and every preview reap", tier, prefix, err)
				}
			}
		}
	})
}
