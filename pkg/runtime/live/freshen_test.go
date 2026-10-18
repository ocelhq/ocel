package live

import (
	"context"
	"testing"
	"time"
)

type expiringSource struct {
	*scriptedSource
	renewWithin time.Duration
	expires     bool
}

func (f expiringSource) RenewWithin() (time.Duration, bool) { return f.renewWithin, f.expires }

func TestFreshenRereadsBeforeItReturnsOnlyWhatAnExpiringSourceHandedOutIsDueForRenewal(t *testing.T) {
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

	t.Run("a source whose values do not expire is refreshed behind the invocation", func(t *testing.T) {
		clock := time.Unix(1_700_000_000, 0)
		release := make(chan struct{})
		inner := resolves(map[string]string{"STORE_BASE": "first"}, map[string]string{"STORE_BASE": "second"})
		source := expiringSource{scriptedSource: inner, renewWithin: 10 * time.Minute}
		l := New(source, []string{"STORE_BASE"}, nil, func() time.Time { return clock })
		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("join: %v", err)
		}

		inner.release = release
		clock = clock.Add(time.Hour)
		l.Freshen(context.Background())

		if got := l.Value("STORE_BASE"); got != "first" {
			t.Errorf("Value() = %q as Freshen returns, want the last generation while the refresh runs behind it", got)
		}
		close(release)
		eventually(t, "the refresh behind the invocation to land", func() bool { return l.Value("STORE_BASE") == "second" })
	})
}
