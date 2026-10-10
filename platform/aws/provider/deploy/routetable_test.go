package deploy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

func edgeRouteTable(key string) *provider.EdgeRouteTable {
	return &provider.EdgeRouteTable{
		Location: router.RouteTableLocation{Format: edge.RouteTableNext, Key: key},
		Table:    []byte(`{"routes":[{"source":"^/(?<slug>[^/]+)$"}]}`),
	}
}

func TestARouteTableSetPutsTheTableAtItsKeyInTheAdoptedCacheStore(t *testing.T) {
	store := &fakeArtifactStore{exists: map[string]bool{}}
	cfg := Config{CacheStoreBucket: "isr", CacheStoreObjects: store}
	table := edgeRouteTable("prod/proj/web/r1a2b3c4d/route-table/ab.json")

	set, err := routeTableSet(cfg, "web", table)
	if err != nil || set == nil {
		t.Fatalf("routeTableSet() = %v, %v, want a set to push", set, err)
	}
	if err := set.push(context.Background(), progress.Discard()); err != nil {
		t.Fatalf("push: %v", err)
	}

	key := table.Location.Key
	if len(store.puts) != 1 || store.puts[0] != key {
		t.Fatalf("uploaded keys = %v, want only %q", store.puts, key)
	}
	if store.buckets[0] != "isr" {
		t.Errorf("uploaded into bucket %q, want the adopted store %q", store.buckets[0], "isr")
	}
	if body := store.putBodies[key]; body != string(table.Table) {
		t.Errorf("uploaded body = %q, want the table verbatim", body)
	}
	if ct := store.contentTypes[key]; ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
}

func TestARouteTableLandsOnlyInTheAssetBucketTaggedEdgeReadableWhenTheEdgeHoldsCredentials(t *testing.T) {
	edgeStore := &fakeArtifactStore{exists: map[string]bool{}}
	assetStore := &fakeArtifactStore{exists: map[string]bool{}}
	cfg := Config{
		CacheStoreBucket: "isr", CacheStoreObjects: edgeStore,
		AssetBucket: "assets", Objects: assetStore,
		EdgeAccessKeyID: "AKIAEDGE", EdgeSecretKey: "secret",
	}
	table := edgeRouteTable("prod/proj/web/r1a2b3c4d/route-table/ab.json")

	set, err := routeTableSet(cfg, "web", table)
	if err != nil || set == nil {
		t.Fatalf("routeTableSet() = %v, %v, want a set to push", set, err)
	}
	said := &recordingProgress{}
	if err := set.push(context.Background(), said); err != nil {
		t.Fatalf("push: %v", err)
	}

	key := table.Location.Key
	if len(assetStore.puts) != 1 || assetStore.puts[0] != key || assetStore.buckets[0] != "assets" {
		t.Errorf("asset bucket was given %v in %v, want %q in assets", assetStore.puts, assetStore.buckets, key)
	}
	if body := assetStore.putBodies[key]; body != string(table.Table) {
		t.Errorf("asset bucket body = %q, want the table verbatim", body)
	}
	if got := assetStore.taggings[key]; got != wantEdgeReadableTagging {
		t.Errorf("tagging = %q, want %q: the edge user reads only objects tagged edge-readable", got, wantEdgeReadableTagging)
	}
	if len(edgeStore.puts) != 0 {
		t.Errorf("adopted store was given %v, want nothing: the edge reads the route table from the asset bucket alone", edgeStore.puts)
	}
	if !slices.ContainsFunc(said.said, func(m string) bool { return strings.HasSuffix(m, "to bucket assets") }) {
		t.Errorf("progress said %q, want it to name the asset bucket the table was written to", said.said)
	}
}

func TestARouteTableLandsInTheAdoptedStoreWhenTheEdgeHoldsNoCredentials(t *testing.T) {
	edgeStore := &fakeArtifactStore{exists: map[string]bool{}}
	assetStore := &fakeArtifactStore{exists: map[string]bool{}}
	cfg := Config{CacheStoreBucket: "isr", CacheStoreObjects: edgeStore, AssetBucket: "assets", Objects: assetStore}
	table := edgeRouteTable("prod/proj/web/r1a2b3c4d/route-table/ab.json")

	set, err := routeTableSet(cfg, "web", table)
	if err != nil || set == nil {
		t.Fatalf("routeTableSet() = %v, %v, want a set to push", set, err)
	}
	if err := set.push(context.Background(), progress.Discard()); err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(edgeStore.puts) != 1 || len(assetStore.puts) != 0 {
		t.Errorf("adopted store was given %v and asset bucket %v, want the table in the adopted store alone", edgeStore.puts, assetStore.puts)
	}
}

func TestAnAppRoutedByNoEdgeRouteTableHasNoRouteTableSet(t *testing.T) {
	set, err := routeTableSet(Config{CacheStoreBucket: "isr", CacheStoreObjects: &fakeArtifactStore{}}, "web", nil)
	if err != nil || set != nil {
		t.Errorf("routeTableSet(nil) = %v, %v, want no set", set, err)
	}
}

func TestARouteTableWithNoAdoptedCacheStoreIsRefusedNamingBootstrap(t *testing.T) {
	_, err := routeTableSet(Config{}, "web", edgeRouteTable("prod/proj/web/r1a2b3c4d/route-table/ab.json"))
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("routeTableSet() = %v, want a not-ready refusal: the edge reads the table from a store this account never adopted", err)
	}
}
