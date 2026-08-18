package cloudfront

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSettler(t *testing.T) {
	t.Parallel()

	t.Run("it polls until the distribution reports Deployed", func(t *testing.T) {
		t.Parallel()

		held := 0
		s := Settler{
			Wait:     func(context.Context, time.Duration) error { held++; return nil },
			Attempts: 5,
			Every:    time.Second,
			Jitter:   func() float64 { return 0.5 },
		}
		polls := 0

		if err := s.settled(context.Background(), "E1", func(context.Context) (string, error) {
			polls++
			if polls < 3 {
				return "InProgress", nil
			}
			return deployedStatus, nil
		}); err != nil {
			t.Fatalf("settled: %v", err)
		}

		if polls != 3 {
			t.Errorf("polls = %d, want 3", polls)
		}
		if held != 2 {
			t.Errorf("holds = %d, want 2: one between each pair of polls", held)
		}
	})

	t.Run("it gives up naming roughly how long it waited", func(t *testing.T) {
		t.Parallel()

		s := Settler{
			Wait:     func(context.Context, time.Duration) error { return nil },
			Attempts: 3,
			Every:    time.Second,
			Jitter:   func() float64 { return 0.5 },
		}

		err := s.settled(context.Background(), "E1", func(context.Context) (string, error) {
			return "InProgress", nil
		})

		if err == nil {
			t.Fatal("settled error = nil, want it to give up")
		}
		for _, want := range []string{"E1", "about " + s.window().String(), "re-run the same command"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %q, want it to contain %q: the jitter makes the window an estimate, and the message must not pretend otherwise", err, want)
			}
		}
	})

	t.Run("a cancelled command stops waiting", func(t *testing.T) {
		t.Parallel()

		s := Settler{
			Wait:     func(context.Context, time.Duration) error { return context.Canceled },
			Attempts: 5,
			Every:    time.Second,
			Jitter:   func() float64 { return 0.5 },
		}

		err := s.settled(context.Background(), "E1", func(context.Context) (string, error) {
			return "InProgress", nil
		})

		if !errors.Is(err, context.Canceled) {
			t.Errorf("settled err = %v, want the cancellation", err)
		}
	})

	t.Run("a throttled status check is another attempt", func(t *testing.T) {
		t.Parallel()

		s := Settler{
			Wait:     func(context.Context, time.Duration) error { return nil },
			Attempts: 5,
			Every:    time.Second,
			Jitter:   func() float64 { return 0.5 },
		}
		polls := 0

		if err := s.settled(context.Background(), "E1", func(context.Context) (string, error) {
			polls++
			if polls < 4 {
				return "", throttlingError()
			}
			return deployedStatus, nil
		}); err != nil {
			t.Fatalf("settled: %v", err)
		}
		if polls != 4 {
			t.Errorf("polls = %d, want 4: a throttle says nothing about the rollout", polls)
		}
	})
}
