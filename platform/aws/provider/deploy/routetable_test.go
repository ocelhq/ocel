package deploy

import (
	"context"
	"errors"
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
