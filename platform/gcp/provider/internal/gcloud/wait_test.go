package gcloud

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAWaitEndsAsSoonAsWhatItWaitsOnIsDone(t *testing.T) {
	asks := 0
	got, err := Until(context.Background(), "the thing", func() (int, error) {
		asks++
		return asks, nil
	}, func(state int) bool { return state == 3 })
	if err != nil || got != 3 || asks != 3 {
		t.Fatalf("until() asked %d times and returned %d, %v, want it to stop at the third answer", asks, got, err)
	}
}

func TestAWaitGivesUpWhenTheRunIsCancelledRatherThanSpin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := Until(ctx, "the thing", func() (int, error) { return 0, nil }, func(int) bool { return false })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("until() = %v, want the run's own deadline", err)
	}
	if !strings.Contains(err.Error(), "the thing") {
		t.Errorf("until() = %q, want it to name what was waited on", err)
	}
}

func TestAWaitSurfacesTheErrorItsReadReturns(t *testing.T) {
	sunk := errors.New("no such thing")
	if _, err := Until(context.Background(), "the thing", func() (int, error) { return 0, sunk },
		func(int) bool { return true }); !errors.Is(err, sunk) {
		t.Fatalf("until() = %v, want the error the read answered with", err)
	}
}
