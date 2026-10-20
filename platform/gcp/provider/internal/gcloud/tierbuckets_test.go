package gcloud

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

type countedReads struct {
	*fake.KeyValues
	mu    sync.Mutex
	reads int
}

func (s *countedReads) Read(ctx context.Context, key records.Key) (records.Entry, error) {
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()
	return s.KeyValues.Read(ctx, key)
}

func (s *countedReads) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

func clientsWithoutStorage(t *testing.T) (*Clients, *countedReads) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("%s %s reached Cloud Storage, want the tier's buckets read from their record alone", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	store := &countedReads{KeyValues: fake.NewKeyValues()}
	return &Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1", Endpoint: server.URL, Records: store}, store
}

func claim(t *testing.T, c *Clients, tier environment.Tier, class naming.StorageClass, bucket string) string {
	t.Helper()
	claimed, err := c.ClaimTierBucket(context.Background(), tier, class, func() string { return bucket })
	if err != nil {
		t.Fatalf("ClaimTierBucket(%s) = %v", class, err)
	}
	return claimed
}

func TestADeployReadsItsTiersBucketsFromOneRecordOnceAndListsNoBucket(t *testing.T) {
	t.Parallel()
	c, store := clientsWithoutStorage(t)
	claim(t, c, environment.TierProduction, naming.StorageState, "ocel-prod-state-k3x9q2")
	claim(t, c, environment.TierProduction, naming.StorageEdge, "ocel-prod-edge-a1b2c3")
	claim(t, c, environment.TierProduction, naming.StorageCache, "ocel-prod-cache-000000")
	c.DropTierBucketsRead(environment.TierProduction)
	before := store.count()

	for range 2 {
		found, err := c.TierBuckets(context.Background(), environment.TierProduction, naming.StorageEdge, naming.StorageCache)
		if err != nil {
			t.Fatalf("TierBuckets() = %v", err)
		}
		if found[naming.StorageEdge] != "ocel-prod-edge-a1b2c3" || found[naming.StorageCache] != "ocel-prod-cache-000000" {
			t.Errorf("TierBuckets() = %v, want the edge and cache buckets the bootstrap recorded", found)
		}
	}
	if reads := store.count() - before; reads != 1 {
		t.Errorf("two reads of the tier's buckets read the record %d times, want once", reads)
	}
}

func TestATierMissingTheBucketACallerNeedsIsRefusedAsNotBootstrappedAndReadAgainNextTime(t *testing.T) {
	t.Parallel()
	c, _ := clientsWithoutStorage(t)
	claim(t, c, environment.TierPreview, naming.StorageState, "ocel-preview-state-k3x9q2")

	if found, err := c.TierBuckets(context.Background(), environment.TierPreview, naming.StorageState); err != nil || found[naming.StorageState] != "ocel-preview-state-k3x9q2" {
		t.Fatalf("TierBuckets(state) of a tier with only its state bucket = %v, %v, want the state bucket", found, err)
	}
	_, err := c.TierBuckets(context.Background(), environment.TierPreview, naming.StorageState, naming.StorageCache)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady || !strings.Contains(err.Error(), "ocel bootstrap") {
		t.Fatalf("TierBuckets(state, cache) of a tier without its cache bucket = %v, want a not-ready refusal naming the bootstrap", err)
	}

	beside := &Clients{Names: c.Names, Region: c.Region, Endpoint: c.Endpoint, Records: c.Records}
	claim(t, beside, environment.TierPreview, naming.StorageCache, "ocel-preview-cache-000000")
	if _, err := c.TierBuckets(context.Background(), environment.TierPreview, naming.StorageCache); err != nil {
		t.Fatalf("TierBuckets(cache) once another run's bootstrap recorded it = %v, want it read", err)
	}
}

func TestTwoBootstrapsClaimingOneClassesBucketBothGetTheNameClaimedFirst(t *testing.T) {
	t.Parallel()
	c, _ := clientsWithoutStorage(t)

	first := claim(t, c, environment.TierProduction, naming.StorageEdge, "ocel-prod-edge-a1b2c3")
	second := claim(t, c, environment.TierProduction, naming.StorageEdge, "ocel-prod-edge-d4e5f6")
	if first != "ocel-prod-edge-a1b2c3" || second != first {
		t.Errorf("the claims got %s and %s, want both the name claimed first, so one bucket is made", first, second)
	}
}

func TestForgettingATierBucketLeavesAnotherRunsClaimInPlace(t *testing.T) {
	t.Parallel()
	c, _ := clientsWithoutStorage(t)
	claim(t, c, environment.TierProduction, naming.StorageEdge, "ocel-prod-edge-a1b2c3")

	if err := c.ForgetTierBucket(context.Background(), environment.TierProduction, naming.StorageEdge, "ocel-prod-edge-d4e5f6"); err != nil {
		t.Fatal(err)
	}
	if found, _ := c.RecordedTierBuckets(context.Background(), environment.TierProduction); found[naming.StorageEdge] != "ocel-prod-edge-a1b2c3" {
		t.Errorf("forgetting a name not recorded left %v, want ocel-prod-edge-a1b2c3 kept", found)
	}
	if err := c.ForgetTierBucket(context.Background(), environment.TierProduction, naming.StorageEdge, "ocel-prod-edge-a1b2c3"); err != nil {
		t.Fatal(err)
	}
	if found, _ := c.RecordedTierBuckets(context.Background(), environment.TierProduction); found[naming.StorageEdge] != "" {
		t.Errorf("forgetting the recorded name left %v, want no edge bucket recorded", found)
	}
}
