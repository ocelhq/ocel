package site

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
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

func TestARouteTableLandsInTheEdgeBucket(t *testing.T) {
	assetStore := &fakeArtifactStore{exists: map[string]bool{}}
	cfg := publication{buckets: Buckets{Buckets: edgeAndCache, Objects: assetStore, EdgeHoldsKey: true}}
	table := edgeRouteTable("prod/proj/web/r1a2b3c4d/route-table/ab.json")

	set, err := routeTableSet(cfg.buckets, "web", table)
	if err != nil || set == nil {
		t.Fatalf("routeTableSet() = %v, %v, want a set to push", set, err)
	}
	said := &recordingProgress{}
	if err := set.push(context.Background(), said); err != nil {
		t.Fatalf("push: %v", err)
	}

	key := table.Location.Key
	if len(assetStore.puts) != 1 || assetStore.puts[0] != key || assetStore.buckets[0] != "edge" {
		t.Errorf("edge bucket was given %v in %v, want %q in edge", assetStore.puts, assetStore.buckets, key)
	}
	if body := assetStore.putBodies[key]; body != string(table.Table) {
		t.Errorf("edge bucket body = %q, want the table verbatim", body)
	}
	if ct := assetStore.contentTypes[key]; ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	if !slices.ContainsFunc(said.said, func(m string) bool { return strings.HasSuffix(m, "to bucket edge") }) {
		t.Errorf("progress said %q, want it to name the edge bucket the table was written to", said.said)
	}
}

func TestARouteTableForAnEdgeHoldingNoKeyToTheEdgeBucketIsRefusedNamingBootstrap(t *testing.T) {
	assetStore := &fakeArtifactStore{exists: map[string]bool{}}
	cfg := publication{buckets: Buckets{Buckets: edgeAndCache, Objects: assetStore}}

	_, err := routeTableSet(cfg.buckets, "web", edgeRouteTable("prod/proj/web/r1a2b3c4d/route-table/ab.json"))

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("routeTableSet() = %v, want a not-ready refusal: the edge reads the table from a bucket it holds no key to", err)
	}
	if len(assetStore.puts) != 0 {
		t.Errorf("edge bucket was given %v, want nothing written to a bucket the edge cannot read", assetStore.puts)
	}
}

func TestAnAppRoutedByNoEdgeRouteTableHasNoRouteTableSet(t *testing.T) {
	set, err := routeTableSet(Buckets{}, "web", nil)
	if err != nil || set != nil {
		t.Errorf("routeTableSet(nil) = %v, %v, want no set", set, err)
	}
}
