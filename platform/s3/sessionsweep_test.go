package s3

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type readCountingStore struct {
	*fakeStore

	mu    sync.Mutex
	lists int
	reads map[string]int
}

func (c *readCountingStore) ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	c.mu.Lock()
	c.lists++
	c.mu.Unlock()
	return c.fakeStore.ListObjectsV2(ctx, in, opts...)
}

func (c *readCountingStore) GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	c.mu.Lock()
	c.reads[aws.ToString(in.Key)]++
	c.mu.Unlock()
	return c.fakeStore.GetObject(ctx, in, opts...)
}

func (c *readCountingStore) counts(key string) (lists, reads int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lists, c.reads[key]
}

func countingReads(h *harness) *readCountingStore {
	counting := &readCountingStore{fakeStore: h.store, reads: map[string]int{}}
	h.svc.cfg.Objects = counting
	return counting
}

func sessionRecords(h *harness) int {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	var records int
	for key := range h.store.objects["store"] {
		if strings.HasPrefix(key, SessionKeyPrefix) {
			records++
		}
	}
	return records
}

func TestASweepNeverRereadsASessionOfABucketNoLongerGranted(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	h.svc.newID = New(h.svc.cfg).newID
	counting := countingReads(h)
	stale := SessionKeyPrefix + "sess_dropped"
	h.store.put("store", stale, []byte(`{"sessionId":"sess_dropped","bucket":"dropped","createdAt":1}`), "application/json")

	for range 3 {
		h.now = h.now.Add(5 * time.Hour)
		h.presign(t, "a.png", 3, "image/png")
	}

	if _, reads := counting.counts(stale); reads != 0 {
		t.Errorf("the sweep read a session of a bucket this app is no longer granted %d times, want none: every hourly sweep re-reads it forever", reads)
	}
}

func TestASweepStopsAtTheFirstSessionYoungerThanItsRecordLifetime(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	h.svc.newID = New(h.svc.cfg).newID
	opened := sweptPerSweep + 500
	for range opened {
		h.presign(t, "a.png", 3, "image/png")
	}
	counting := countingReads(h)
	h.now = h.now.Add(sweepInterval)

	h.presign(t, "a.png", 3, "image/png")

	if lists, _ := counting.counts(""); lists != 1 {
		t.Errorf("a sweep over %d sessions all younger than their record lifetime listed %d pages, want 1: it reads past the young ones to its cap", opened, lists)
	}
	if kept := sessionRecords(h); kept != opened+1 {
		t.Errorf("%d session records are kept, want all %d", kept, opened+1)
	}
}

func TestASweepSettlesTheOldestSessionsFirstWhateverTheirIDs(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	h.svc.newID = New(h.svc.cfg).newID
	h.presign(t, "a.png", 3, "image/png")
	h.now = h.now.Add(sessionRecordLifetime + time.Hour)
	young := sweptPerSweep*sweepPageLimit + 1
	for range young {
		h.presign(t, "b.png", 3, "image/png")
	}
	h.now = h.now.Add(sweepInterval)

	h.presign(t, "b.png", 3, "image/png")

	if kept := sessionRecords(h); kept != young+1 {
		t.Errorf("%d session records are kept, want only the %d young ones: an old session sorted behind the sweep's cap is never settled", kept, young+1)
	}
}
