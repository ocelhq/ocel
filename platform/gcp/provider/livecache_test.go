//go:build integration

package gcp_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestLivePruningADeploymentRemovesItsCacheEntriesAndTagSnapshotAndKeepsAnothers(t *testing.T) {
	p := live(t)
	bootstrappedTiers(t, p)

	ctx := context.Background()
	artifacts := p.Artifacts()
	tier := environment.TierProduction
	slug := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))

	pruned := []string{
		"prod/" + slug + "/web/r1/isr/cache/index.cache.json",
		"prod/" + slug + "/web/r1/isr/tag-clock.json",
		"prod/" + slug + "/web/r1/isr/use-cache/abc.json",
	}
	otherRelease := "prod/" + slug + "/web/r2/isr/tag-clock.json"
	preview := "pr-7/" + slug + "/web/r1/isr/tag-clock.json"

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
