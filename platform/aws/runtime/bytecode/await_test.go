package bytecode

import (
	"context"
	"testing"
	"time"
)

func neverEmbedded(context.Context, *Cache) bool { return false }

func priming(resolved *Cache, embedded, rehydrate func(context.Context, *Cache) bool) *Priming {
	ready := make(chan *Cache, 1)
	ready <- resolved
	return &Priming{started: time.Now(), resolved: ready, embedded: embedded, rehydrate: rehydrate}
}

func TestAwait(t *testing.T) {
	t.Run("an embedded hit skips the S3 leg", func(t *testing.T) {
		rehydrateCalls := 0
		p := priming(&Cache{store: &fakeStore{}, bucket: "assets-xyz", key: "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz"},
			func(context.Context, *Cache) bool { return true },
			func(context.Context, *Cache) bool { rehydrateCalls++; return true })

		got := p.Await(context.Background())

		if rehydrateCalls != 0 {
			t.Errorf("rehydrate calls = %d, want the S3 leg skipped entirely", rehydrateCalls)
		}
		if !got.Cached() {
			t.Error("Cached() = false, want the hit recorded")
		}
		if got.Source() != SourceEmbedded {
			t.Errorf("Source() = %q, want %q", got.Source(), SourceEmbedded)
		}
		if got.Key() != "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz" {
			t.Errorf("Key() = %q, want the resolution's", got.Key())
		}
	})

	t.Run("no embedded copy falls back to S3", func(t *testing.T) {
		p := priming(&Cache{store: &fakeStore{}, bucket: "b", key: "k"}, neverEmbedded,
			func(context.Context, *Cache) bool { return true })

		if got := p.Await(context.Background()); got.Source() != SourceS3 {
			t.Errorf("Source() = %q, want %q", got.Source(), SourceS3)
		}
	})

	t.Run("miss on both legs reports no source", func(t *testing.T) {
		p := priming(&Cache{store: &fakeStore{}, bucket: "b", key: "k"}, neverEmbedded,
			func(context.Context, *Cache) bool { return false })

		got := p.Await(context.Background())
		if got == nil {
			t.Fatal("Await() = nil, want the cache armed for an upload after a miss")
		}
		if got.Source() != SourceNone {
			t.Errorf("Source() = %q, want %q", got.Source(), SourceNone)
		}
		if got.Cached() {
			t.Error("Cached() = true, want a miss on both legs")
		}
	})

	t.Run("nil resolution skips both legs", func(t *testing.T) {
		embeddedCalls, rehydrateCalls := 0, 0
		p := priming(nil,
			func(context.Context, *Cache) bool { embeddedCalls++; return true },
			func(context.Context, *Cache) bool { rehydrateCalls++; return true })

		if got := p.Await(context.Background()); got != nil {
			t.Errorf("Await() = %+v, want nil for an unconfigured deployment", got)
		}
		if embeddedCalls != 0 || rehydrateCalls != 0 {
			t.Errorf("legs run = %d embedded, %d s3, want none for an unconfigured deployment", embeddedCalls, rehydrateCalls)
		}
	})

	t.Run("a resolution that never arrives does not block", func(t *testing.T) {
		rehydrateCalls := 0
		p := &Priming{
			started:   time.Now(),
			resolved:  make(chan *Cache),
			embedded:  neverEmbedded,
			rehydrate: func(context.Context, *Cache) bool { rehydrateCalls++; return false },
		}

		done := make(chan *Cache, 1)
		go func() { done <- p.Await(context.Background()) }()

		select {
		case got := <-done:
			if got != nil {
				t.Errorf("Await() = %+v, want nil when the resolution never arrived", got)
			}
		case <-time.After(resolveBudget + 2*time.Second):
			t.Fatal("Await never returned; a resolution that never arrived blocked the spawn")
		}
		if rehydrateCalls != 0 {
			t.Errorf("rehydrate calls = %d, want 0 when the resolution never arrived", rehydrateCalls)
		}
	})

	t.Run("a failed embedded attempt leaves the S3 leg the remaining budget", func(t *testing.T) {
		const embeddedCost = 300 * time.Millisecond

		var remaining time.Duration
		p := priming(&Cache{store: &fakeStore{}, bucket: "b", key: "k"},
			func(context.Context, *Cache) bool {
				time.Sleep(embeddedCost)
				return false
			},
			func(ctx context.Context, _ *Cache) bool {
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Error("the S3 leg was handed a context with no deadline, want the shared rehydrate budget")
					return false
				}
				remaining = time.Until(deadline)
				return true
			})

		p.Await(context.Background())

		if remaining > rehydrateBudget-embeddedCost/2 {
			t.Errorf("S3 leg budget = %s, want it short by the %s the embedded attempt spent", remaining, embeddedCost)
		}
		if remaining <= 0 {
			t.Errorf("S3 leg budget = %s, want what remains of the shared budget", remaining)
		}
	})
}
