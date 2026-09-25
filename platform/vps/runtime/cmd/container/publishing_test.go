package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

type stalling struct {
	fetches atomic.Int32
	held    chan struct{}
}

func (s *stalling) Fetch(ctx context.Context) (map[string]string, error) {
	s.fetches.Add(1)
	if s.held == nil {
		return map[string]string{"SOMETHING": "live"}, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.held:
		return map[string]string{"SOMETHING": "live"}, nil
	}
}

func TestAStoreWithNoClaimedAddressAnswersOnTheCallersOwnDeadline(t *testing.T) {
	t.Parallel()

	source := &stalling{}
	clock := time.Unix(1_000_000, 0)
	values := live.New(source, []string{"SOMETHING"}, nil, func() time.Time { return clock })
	if err := <-values.Prefetch(context.Background()); err != nil {
		t.Fatalf("Prefetch() = %v", err)
	}
	source.held = make(chan struct{})
	defer close(source.held)
	clock = clock.Add(time.Minute)

	external := publishing(s3store.Store{}, values)
	gone, cancel := context.WithCancel(context.Background())
	cancel()

	settled := make(chan time.Duration, 1)
	go func() {
		began := time.Now()
		_, base := external(gone)
		if base != "" {
			t.Errorf("a box claiming nothing for its store answered %q as its public address", base)
		}
		settled <- time.Since(began)
	}()

	select {
	case took := <-settled:
		if took > time.Second {
			t.Errorf("a signing call took %v on a box claiming nothing, so every external sign waits on a reread the caller already gave up on", took)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a signing call never answered on a box claiming nothing for its store")
	}
}

func TestAStoreWithNoClaimedAddressIsNotRereadOnEveryCall(t *testing.T) {
	t.Parallel()

	source := &stalling{}
	clock := time.Unix(2_000_000, 0)
	values := live.New(source, []string{"SOMETHING"}, nil, func() time.Time { return clock })
	if err := <-values.Prefetch(context.Background()); err != nil {
		t.Fatalf("Prefetch() = %v", err)
	}

	external := publishing(s3store.Store{}, values)
	for range 5 {
		clock = clock.Add(time.Minute)
		if _, base := external(context.Background()); base != "" {
			t.Fatalf("base = %q, want nothing", base)
		}
	}
	if held := source.fetches.Load(); held > 2 {
		t.Errorf("a box claiming nothing was reread %d times over five signing calls, so every call pays for the same empty answer", held-1)
	}
}
