package providerkit

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

type countingStore struct {
	mu     sync.Mutex
	live   int
	peak   int
	arrive chan struct{}
	hold   chan struct{}
}

func (s *countingStore) Put(context.Context, ArtifactRef, io.Reader) error { return nil }

func (s *countingStore) Has(context.Context, ArtifactRef) (bool, error) {
	s.mu.Lock()
	s.live++
	if s.live > s.peak {
		s.peak = s.live
	}
	s.mu.Unlock()

	s.arrive <- struct{}{}
	<-s.hold

	s.mu.Lock()
	s.live--
	s.mu.Unlock()
	return true, nil
}

func (s *countingStore) Open(context.Context, ArtifactRef) (io.ReadCloser, error) {
	return nil, os.ErrNotExist
}

func (s *countingStore) RemovePrefix(context.Context, Class, string, Reporter) error { return nil }

func uploadsOf(t *testing.T, count int) []Upload {
	t.Helper()
	dir := t.TempDir()
	uploads := make([]Upload, 0, count)
	for slot := range count {
		name := strconv.Itoa(slot)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		uploads = append(uploads, Upload{Name: name, Path: path, Ref: ArtifactRef{Key: name}})
	}
	return uploads
}

func TestShipUploadsSharesOneBudgetAcrossTheAppsShippingAtOnce(t *testing.T) {
	const apps = 4

	uploads := uploadsOf(t, uploadConcurrency)
	store := &countingStore{
		arrive: make(chan struct{}, apps*len(uploads)),
		hold:   make(chan struct{}),
	}

	var group sync.WaitGroup
	failures := make([]error, apps)
	for slot := range apps {
		group.Add(1)
		go func() {
			defer group.Done()
			failures[slot] = ShipUploads(context.Background(), store, uploads, nil)
		}()
	}

	for range uploadConcurrency {
		<-store.arrive
	}
	select {
	case <-store.arrive:
		t.Error("an upload started while the budget was already full: each app took a budget of its own")
	case <-time.After(200 * time.Millisecond):
	}
	close(store.hold)
	group.Wait()

	for slot, err := range failures {
		if err != nil {
			t.Fatalf("ShipUploads() for app %d = %v", slot, err)
		}
	}
	if store.peak > uploadConcurrency {
		t.Errorf("%d uploads were in flight at once, want at most %d: the apps standing up side by side share one budget rather than each taking a full one", store.peak, uploadConcurrency)
	}
}
