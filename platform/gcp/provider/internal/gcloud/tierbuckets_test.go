package gcloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

type bucketListing struct {
	mu       sync.Mutex
	buckets  []string
	prefixes []string
}

func (l *bucketListing) open(t *testing.T) *Clients {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/storage/v1/b" {
			t.Errorf("%s %s, want only bucket listings", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		prefix := r.URL.Query().Get("prefix")
		l.prefixes = append(l.prefixes, prefix)
		var items []map[string]string
		for _, bucket := range l.buckets {
			if strings.HasPrefix(bucket, prefix) {
				items = append(items, map[string]string{"name": bucket})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"items": items})
	}))
	t.Cleanup(server.Close)
	return &Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1", Endpoint: server.URL}
}

func (l *bucketListing) listed() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.prefixes...)
}

func TestATiersBucketsAreFoundByOneListingUnderTheTierPrefixAndRemembered(t *testing.T) {
	t.Parallel()
	listing := &bucketListing{buckets: []string{
		"ocel-prod-state-k3x9q2", "ocel-prod-edge-a1b2c3", "ocel-prod-cache-000000",
		"ocel-preview-edge-zzzzzz", "ocel-prod-edge", "ocel-prod-edgy-a1b2c3", "ocel-app-shop-prod-edge-a1b2c3",
	}}
	c := listing.open(t)

	for range 2 {
		found, err := c.TierBuckets(context.Background(), environment.TierProduction)
		if err != nil {
			t.Fatalf("TierBuckets() = %v", err)
		}
		if want := (names.TierBuckets{State: "ocel-prod-state-k3x9q2", Edge: "ocel-prod-edge-a1b2c3", Cache: "ocel-prod-cache-000000"}); found != want {
			t.Errorf("TierBuckets() = %+v, want %+v", found, want)
		}
	}
	if got := listing.listed(); len(got) != 1 || got[0] != "ocel-prod-" {
		t.Errorf("listed under %q, want one listing under %q", got, "ocel-prod-")
	}
}

func TestATierMissingABucketIsRefusedAsNotBootstrappedAndListedAgainNextTime(t *testing.T) {
	t.Parallel()
	listing := &bucketListing{buckets: []string{"ocel-preview-state-k3x9q2", "ocel-preview-edge-a1b2c3"}}
	c := listing.open(t)

	_, err := c.TierBuckets(context.Background(), environment.TierPreview)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady || !strings.Contains(err.Error(), "ocel bootstrap") {
		t.Fatalf("TierBuckets() of a tier without its cache bucket = %v, want a not-ready refusal naming the bootstrap", err)
	}

	listing.mu.Lock()
	listing.buckets = append(listing.buckets, "ocel-preview-cache-000000")
	listing.mu.Unlock()
	if _, err := c.TierBuckets(context.Background(), environment.TierPreview); err != nil {
		t.Fatalf("TierBuckets() once the bootstrap made the bucket = %v, want it found", err)
	}
}

func TestATierHoldingTwoBucketsOfOneClassIsRefused(t *testing.T) {
	t.Parallel()
	listing := &bucketListing{buckets: []string{
		"ocel-prod-state-k3x9q2", "ocel-prod-edge-a1b2c3", "ocel-prod-edge-d4e5f6", "ocel-prod-cache-000000",
	}}
	c := listing.open(t)

	_, err := c.TierBuckets(context.Background(), environment.TierProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "ocel-prod-edge-a1b2c3") || !strings.Contains(err.Error(), "ocel-prod-edge-d4e5f6") {
		t.Fatalf("TierBuckets() = %v, want a refusal naming both edge buckets", err)
	}
}

func TestOneClassesBucketIsFoundByAListingUnderThatClassAlone(t *testing.T) {
	t.Parallel()
	listing := &bucketListing{buckets: []string{"ocel-prod-state-k3x9q2", "ocel-prod-edge-a1b2c3"}}
	c := listing.open(t)

	found, err := c.FindTierBucket(context.Background(), environment.TierProduction, naming.StorageState)
	if err != nil || found == nil || found.Name != "ocel-prod-state-k3x9q2" {
		t.Fatalf("FindTierBucket(state) = %+v, %v, want ocel-prod-state-k3x9q2", found, err)
	}
	absent, err := c.FindTierBucket(context.Background(), environment.TierProduction, naming.StorageCache)
	if err != nil || absent != nil {
		t.Fatalf("FindTierBucket(cache) = %+v, %v, want nothing found", absent, err)
	}
	if got := listing.listed(); len(got) != 2 || got[0] != "ocel-prod-state-" || got[1] != "ocel-prod-cache-" {
		t.Errorf("listed under %q, want one listing under each class's untagged name", got)
	}
}
