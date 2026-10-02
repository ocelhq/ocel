package runs_test

import (
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

func TestTheBackoffDoublesFromTheMinimumDelayUpToTheMaximumWithJitterInItsUpperHalf(t *testing.T) {
	t.Parallel()

	policy := provider.RetryPolicy{MaxAttempts: 10, MinDelay: time.Second, MaxDelay: 5 * time.Second}
	for _, tc := range []struct {
		attempt int
		random  float64
		want    time.Duration
	}{
		{1, 1, time.Second},
		{1, 0, 500 * time.Millisecond},
		{2, 1, 2 * time.Second},
		{3, 1, 4 * time.Second},
		{4, 1, 5 * time.Second},
		{4, 0, 2500 * time.Millisecond},
		{30, 1, 5 * time.Second},
	} {
		if got := runs.Backoff(policy, tc.attempt, tc.random); got != tc.want {
			t.Errorf("Backoff(attempt %d, random %v) = %v, want %v", tc.attempt, tc.random, got, tc.want)
		}
	}
}

func TestAJitterIsAFractionFromZeroUpToOne(t *testing.T) {
	t.Parallel()

	for range 100 {
		if jitter := runs.Jitter(); jitter < 0 || jitter >= 1 {
			t.Fatalf("Jitter() = %v, want it in [0, 1)", jitter)
		}
	}
}

func TestARunTakesTheAttemptsItAsksForUpToItsConsumersMaximum(t *testing.T) {
	t.Parallel()

	policy := provider.RetryPolicy{MaxAttempts: 5}
	for requested, want := range map[int32]int{0: 5, 2: 2, 5: 5, 9: 5, -1: 5} {
		if got := runs.AttemptsFor(policy, requested); got != want {
			t.Errorf("AttemptsFor(max 5, requested %d) = %d, want %d", requested, got, want)
		}
	}
}
