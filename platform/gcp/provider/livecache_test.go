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
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestLivePruningADeploymentRemovesItsCacheEntriesAndKeepsAnothers(t *testing.T) {
	p := live(t)
	bootstrappedTiers(t, p)

	ctx := context.Background()
	artifacts := p.Artifacts()
	tier := environment.TierProduction
	slug := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))

	pruned := []string{
		"prod/" + slug + "/web/r1/isr/cache/index.cache.json",
		"prod/" + slug + "/web/r1/isr/use-cache/abc.json",
	}
	otherRelease := "prod/" + slug + "/web/r2/isr/cache/index.cache.json"
	preview := "pr-7/" + slug + "/web/r1/isr/cache/index.cache.json"

	has := func(key string) bool {
		t.Helper()
		found, err := artifacts.Has(ctx, provider.ArtifactRef{Tier: tier, Bucket: provider.StoreCache, Key: key})
		if err != nil {
			t.Fatalf("Has(%s) = %v", key, err)
		}
		return found
	}

	for _, key := range append([]string{otherRelease, preview}, pruned...) {
		ref := provider.ArtifactRef{Tier: tier, Bucket: provider.StoreCache, Key: key}
		if err := artifacts.Put(ctx, ref, bytes.NewReader([]byte("{}"))); err != nil {
			t.Fatalf("Put(%s) = %v", key, err)
		}
	}
	t.Cleanup(func() {
		_ = artifacts.RemovePrefix(ctx, tier, "prod/"+slug+"/", nil)
		_ = artifacts.RemovePrefix(ctx, tier, "pr-7/"+slug+"/", nil)
	})

	if err := artifacts.RemovePrefix(ctx, tier, "prod/"+slug+"/web/r1/isr/", nil); err != nil {
		t.Fatalf("RemovePrefix(r1 isr) = %v", err)
	}
	for _, key := range pruned {
		if has(key) {
			t.Errorf("Has(%s) = true, want the pruned release's cache object deleted", key)
		}
	}
	for _, key := range []string{otherRelease, preview} {
		if !has(key) {
			t.Errorf("Has(%s) = false, want another deployment's object left alone", key)
		}
	}

	if err := artifacts.RemovePrefix(ctx, tier, "pr-7/"+slug+"/", nil); err != nil {
		t.Fatalf("RemovePrefix(pr-7) = %v", err)
	}
	if has(preview) {
		t.Errorf("Has(%s) = true, want the preview environment's object deleted", preview)
	}
	if !has(otherRelease) {
		t.Errorf("Has(%s) = false, want the production release left alone", otherRelease)
	}
}

func TestLivePruningADeploymentRemovesItsTagRecordsAndKeepsAnothers(t *testing.T) {
	p := live(t)
	bootstrappedTiers(t, p)

	ctx := context.Background()
	tier := environment.TierProduction
	slug := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	store, err := workloadClients(t).TagFirestore(tier)
	if err != nil {
		t.Fatalf("TagFirestore() = %v", err)
	}

	pruned := "prod/" + slug + "/web/r1/isr/"
	otherRelease := "prod/" + slug + "/web/r2/isr/"
	preview := "pr-7/" + slug + "/web/r1/isr/"

	id := func(prefix string) string {
		sum := sha256.Sum256([]byte(prefix + "tag"))
		return hex.EncodeToString(sum[:])
	}
	for _, prefix := range []string{pruned, otherRelease, preview} {
		if _, err := store.Collection("tags").Doc(id(prefix)).Set(ctx, map[string]any{"prefix": prefix, "tag": "tag", "stale": 1, "expired": 0}); err != nil {
			t.Fatalf("write the tag record of %s = %v", prefix, err)
		}
	}
	t.Cleanup(func() {
		_ = p.Artifacts().RemovePrefix(ctx, tier, "prod/"+slug+"/", nil)
		_ = p.Artifacts().RemovePrefix(ctx, tier, "pr-7/"+slug+"/", nil)
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

	if err := p.Artifacts().RemovePrefix(ctx, tier, pruned, nil); err != nil {
		t.Fatalf("RemovePrefix(r1 isr) = %v", err)
	}
	if has(pruned) {
		t.Errorf("the tag record of %s remains, want it deleted with its release's cache", pruned)
	}
	for _, prefix := range []string{otherRelease, preview} {
		if !has(prefix) {
			t.Errorf("the tag record of %s is gone, want another deployment's record left alone", prefix)
		}
	}

	if err := p.Artifacts().RemovePrefix(ctx, tier, "pr-7/"+slug+"/", nil); err != nil {
		t.Fatalf("RemovePrefix(pr-7) = %v", err)
	}
	if has(preview) {
		t.Errorf("the tag record of %s remains, want the preview environment's records deleted", preview)
	}
	if !has(otherRelease) {
		t.Errorf("the tag record of %s is gone, want the production release's record left alone", otherRelease)
	}
}
