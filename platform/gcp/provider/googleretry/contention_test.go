package googleretry

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAWriteContendedForAMinuteIsMadeOnceTheOtherWritersAreDone(t *testing.T) {
	t.Parallel()

	for name, conflict := range map[string]error{
		"url map fingerprint": &googleapi.Error{Code: http.StatusPreconditionFailed},
		"iam policy etag":     &googleapi.Error{Code: http.StatusConflict},
		"grpc aborted":        status.Error(codes.Aborted, "There were concurrent policy changes"),
	} {
		for jitter, fraction := range map[string]float64{"longest waits": 1, "half waits": 0.5} {
			t.Run(name+", "+jitter, func(t *testing.T) {
				t.Parallel()
				f := newFakeClock()
				f.fraction = fraction
				settled := f.at.Add(time.Minute)

				writes := 0
				err := untilWritten(context.Background(), f.clock(), func() error {
					writes++
					if f.at.Before(settled) {
						return conflict
					}
					return nil
				})
				if err != nil {
					t.Fatalf("untilWritten() = %v after %d writes over %v, want the write made once a minute of other writers was over", err, writes, f.total())
				}
				if f.total() > 2*time.Minute {
					t.Errorf("untilWritten() waited %v for writers that were done within a minute", f.total())
				}
			})
		}
	}
}

func TestAWriteContendedThroughoutGivesUpWithTheConflictWithinFiveMinutes(t *testing.T) {
	t.Parallel()
	f := newFakeClock()
	conflict := &googleapi.Error{Code: http.StatusPreconditionFailed}

	writes := 0
	err := untilWritten(context.Background(), f.clock(), func() error {
		writes++
		return conflict
	})
	if !errors.Is(err, conflict) {
		t.Fatalf("untilWritten() = %v, want the conflict once the wait ran out", err)
	}
	if f.total() < 4*time.Minute || f.total() > 5*time.Minute {
		t.Errorf("untilWritten() waited %v over %d writes, want it to give up within 5m0s but not much sooner", f.total(), writes)
	}
}

func TestAContendedWriteStopsWhenTheRunIsCancelled(t *testing.T) {
	t.Parallel()
	f := newFakeClock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.sleeping = func(waits int) {
		if waits == 2 {
			cancel()
		}
	}

	writes := 0
	err := untilWritten(ctx, f.clock(), func() error {
		writes++
		return &googleapi.Error{Code: http.StatusConflict}
	})
	if !errors.Is(err, context.Canceled) || writes != 3 {
		t.Errorf("untilWritten() made %d writes and returned %v, want 3 writes and the run's cancellation", writes, err)
	}
}

func TestAContendedWriteAnswersWithTheConflictRatherThanWaitPastTheRunsDeadline(t *testing.T) {
	t.Parallel()
	f := newFakeClock()
	f.at = time.Now()
	ctx, cancel := context.WithDeadline(context.Background(), f.at.Add(10*time.Second))
	defer cancel()
	conflict := &googleapi.Error{Code: http.StatusConflict}

	err := untilWritten(ctx, f.clock(), func() error { return conflict })
	if !errors.Is(err, conflict) || f.total() > 10*time.Second {
		t.Errorf("untilWritten() = %v after waiting %v, want the conflict before the run's deadline 10s away", err, f.total())
	}
}

func TestAWriteRefusedForAnythingButContentionIsMadeOnce(t *testing.T) {
	t.Parallel()
	f := newFakeClock()
	refused := &googleapi.Error{Code: http.StatusForbidden}

	writes := 0
	err := untilWritten(context.Background(), f.clock(), func() error {
		writes++
		return refused
	})
	if !errors.Is(err, refused) || writes != 1 {
		t.Errorf("untilWritten() made %d writes and returned %v, want one write: only contention passes", writes, err)
	}
}
