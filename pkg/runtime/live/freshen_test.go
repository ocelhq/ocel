package live

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type expiringSource struct {
	*scriptedSource
	renewWithin time.Duration
	expires     bool
}

func (f expiringSource) RenewWithin() (time.Duration, bool) { return f.renewWithin, f.expires }

func TestFreshenRereadsStaleValuesAndCredentialsDueForRenewalBeforeReturning(t *testing.T) {
	t.Run("values an expiring source handed out past their renewal age are read again before Freshen returns", func(t *testing.T) {
		clock := time.Unix(1_700_000_000, 0)
		source := expiringSource{
			scriptedSource: resolves(map[string]string{"DB_TOKEN": "minted-first"}, map[string]string{"DB_TOKEN": "minted-again"}),
			renewWithin:    10 * time.Minute,
			expires:        true,
		}
		l := New(source, []string{"DB_TOKEN"}, nil, func() time.Time { return clock })
		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		clock = clock.Add(10 * time.Minute)
		l.Freshen(context.Background())

		if got := l.Value("DB_TOKEN"); got != "minted-again" {
			t.Errorf("Value() = %q as Freshen returns, want the token read again before the invocation runs on it", got)
		}
	})

	t.Run("values younger than the staleness bound cost no read", func(t *testing.T) {
		clock := time.Unix(1_700_000_000, 0)
		source := expiringSource{
			scriptedSource: resolves(map[string]string{"DB_TOKEN": "minted-first"}, map[string]string{"DB_TOKEN": "minted-again"}),
			renewWithin:    10 * time.Minute,
			expires:        true,
		}
		l := New(source, []string{"DB_TOKEN"}, nil, func() time.Time { return clock })
		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		clock = clock.Add(StalenessBound - time.Second)
		l.Freshen(context.Background())

		if source.startedFetches() != 1 {
			t.Errorf("the store was read %d times, want once: the token is still young", source.startedFetches())
		}
	})

	t.Run("an invocation waits for a stale secret to reach the app before it can finish and freeze the runtime", func(t *testing.T) {
		clock := time.Unix(1_700_000_000, 0)
		release := make(chan struct{})
		inner := resolves(map[string]string{"STORE_BASE": "first"}, map[string]string{"STORE_BASE": "second"})
		source := expiringSource{scriptedSource: inner, renewWithin: 10 * time.Minute}
		l := New(source, []string{"STORE_BASE"}, nil, func() time.Time { return clock })
		dir := t.TempDir()
		if err := l.Project(dir); err != nil {
			t.Fatal(err)
		}
		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		inner.release = release
		clock = clock.Add(StalenessBound)
		done := make(chan struct{})
		go func() {
			l.Freshen(context.Background())
			close(done)
		}()
		eventually(t, "the stale secret fetch to start", func() bool { return inner.startedFetches() == 2 })
		select {
		case <-done:
			close(release)
			t.Fatal("Freshen returned before the store read completed, so Lambda can freeze it and serve the old secret indefinitely")
		case <-time.After(20 * time.Millisecond):
		}
		close(release)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Freshen did not return after the store read completed")
		}
		if got := l.Value("STORE_BASE"); got != "second" {
			t.Errorf("Value() = %q as Freshen returns, want the current secret before the request reaches the app", got)
		}
		value, err := os.ReadFile(filepath.Join(dir, "STORE_BASE"))
		if err != nil {
			t.Fatal(err)
		}
		if string(value) != "second" {
			t.Errorf("the app's live file = %q, want the current secret before the request reaches the app", value)
		}
	})
}
