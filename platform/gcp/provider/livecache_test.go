//go:build integration

package gcp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

func scopeOf(t *testing.T) func(naming.StorageScope, error) naming.StorageScope {
	return func(scope naming.StorageScope, err error) naming.StorageScope {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return scope
	}
}

func liveReleases(t *testing.T) (pruned, otherRelease, preview naming.Coordinate) {
	slug := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	pruned = naming.Coordinate{Project: slug, Env: "prod", App: "web", Release: naming.NewReleaseToken("b1", "")}
	otherRelease = pruned
	otherRelease.Release = naming.NewReleaseToken("b2", "")
	preview = pruned
	preview.Env = "pr-7"
	return pruned, otherRelease, preview
}

func TestLivePruningADeploymentRemovesItsCacheEntriesAndKeepsAnothers(t *testing.T) {
	p := live(t)
	bootstrappedTiers(t, p)

	ctx := context.Background()
	artifacts := p.Artifacts()
	tier := environment.TierProduction
	release, otherRelease, preview := liveReleases(t)
	project := scopeOf(t)(naming.ProjectScope(release.Project))

	pruned := []string{
		release.ObjectKey(naming.ObjectPages, "index.cache.json"),
		release.ObjectKey(naming.ObjectUseCache, "abc.json"),
	}
	kept := otherRelease.ObjectKey(naming.ObjectPages, "index.cache.json")
	previewed := preview.ObjectKey(naming.ObjectPages, "index.cache.json")

	has := func(key string) bool {
		t.Helper()
		found, err := artifacts.Has(ctx, provider.ArtifactRef{Tier: tier, Key: key})
		if err != nil {
			t.Fatalf("Has(%s) = %v", key, err)
		}
		return found
	}

	for _, key := range append([]string{kept, previewed}, pruned...) {
		if err := artifacts.Put(ctx, provider.ArtifactRef{Tier: tier, Key: key}, bytes.NewReader([]byte("{}"))); err != nil {
			t.Fatalf("Put(%s) = %v", key, err)
		}
	}
	t.Cleanup(func() {
		_ = artifacts.RemoveObjects(ctx, tier, project, naming.CoordinateObjectKinds, nil)
	})

	if err := artifacts.RemoveObjects(ctx, tier, scopeOf(t)(naming.ReleaseScope(release)), naming.ISRObjectKinds, nil); err != nil {
		t.Fatalf("RemoveObjects(r1) = %v", err)
	}
	for _, key := range pruned {
		if has(key) {
			t.Errorf("Has(%s) = true, want the pruned release's cache object deleted", key)
		}
	}
	for _, key := range []string{kept, previewed} {
		if !has(key) {
			t.Errorf("Has(%s) = false, want another deployment's object left alone", key)
		}
	}

	if err := artifacts.RemoveObjects(ctx, tier, scopeOf(t)(naming.EnvScope(preview.Project, preview.Env)), naming.CoordinateObjectKinds, nil); err != nil {
		t.Fatalf("RemoveObjects(pr-7) = %v", err)
	}
	if has(previewed) {
		t.Errorf("Has(%s) = true, want the preview environment's object deleted", previewed)
	}
	if !has(kept) {
		t.Errorf("Has(%s) = false, want the production release left alone", kept)
	}
}

func TestLivePruningADeploymentRemovesItsTagRecordsAndKeepsAnothers(t *testing.T) {
	p := live(t)
	bootstrappedTiers(t, p)

	ctx := context.Background()
	tier := environment.TierProduction
	release, otherRelease, preview := liveReleases(t)
	writableDatabaseMade(t, p, tier, release.Project)
	store, err := workloadClients(t).TagRecordsFirestore(tier, release.Project)
	if err != nil {
		t.Fatalf("TagRecordsFirestore() = %v", err)
	}

	pruned := release.StoragePrefix(naming.ObjectPages)
	kept := otherRelease.StoragePrefix(naming.ObjectPages)
	previewed := preview.StoragePrefix(naming.ObjectPages)

	id := func(prefix string) string {
		sum := sha256.Sum256([]byte(prefix + "tag"))
		return hex.EncodeToString(sum[:])
	}
	for _, prefix := range []string{pruned, kept, previewed} {
		if _, err := store.Collection("tags").Doc(id(prefix)).Set(ctx, map[string]any{"prefix": prefix, "tag": "tag", "stale": 1, "expired": 0}); err != nil {
			t.Fatalf("write the tag record of %s = %v", prefix, err)
		}
	}
	t.Cleanup(func() {
		_ = p.Artifacts().RemoveObjects(ctx, tier, scopeOf(t)(naming.ProjectScope(release.Project)), naming.CoordinateObjectKinds, nil)
	})
	has := func(prefix string) bool {
		t.Helper()
		_, err := store.Collection("tags").Doc(id(prefix)).Get(ctx)
		if status.Code(err) == codes.NotFound {
			return false
		}
		if err != nil {
			t.Fatalf("read the tag record of %s = %v", prefix, err)
		}
		return true
	}

	if err := p.Artifacts().RemoveObjects(ctx, tier, scopeOf(t)(naming.ReleaseScope(release)), naming.ISRObjectKinds, nil); err != nil {
		t.Fatalf("RemoveObjects(r1) = %v", err)
	}
	if has(pruned) {
		t.Errorf("the tag record of %s remains, want it deleted with its release's cache", pruned)
	}
	for _, prefix := range []string{kept, previewed} {
		if !has(prefix) {
			t.Errorf("the tag record of %s is gone, want another deployment's record left alone", prefix)
		}
	}

	if err := p.Artifacts().RemoveObjects(ctx, tier, scopeOf(t)(naming.EnvScope(preview.Project, preview.Env)), naming.CoordinateObjectKinds, nil); err != nil {
		t.Fatalf("RemoveObjects(pr-7) = %v", err)
	}
	if has(previewed) {
		t.Errorf("the tag record of %s remains, want the preview environment's records deleted", previewed)
	}
	if !has(kept) {
		t.Errorf("the tag record of %s is gone, want the production release's record left alone", kept)
	}
}
