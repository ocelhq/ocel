package googleretry

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

type fakeClock struct {
	at       time.Time
	waited   []time.Duration
	fraction float64
	sleeping func(waits int)
}

func newFakeClock() *fakeClock {
	return &fakeClock{at: time.Date(2026, 10, 25, 12, 0, 15, 0, time.UTC), fraction: 1}
}

func (f *fakeClock) clock() clock {
	return clock{
		now: func() time.Time { return f.at },
		sleep: func(ctx context.Context, wait time.Duration) bool {
			if f.sleeping != nil {
				f.sleeping(len(f.waited))
			}
			if ctx.Err() != nil {
				return false
			}
			f.waited = append(f.waited, wait)
			f.at = f.at.Add(wait)
			return true
		},
		random: func(limit time.Duration) time.Duration { return time.Duration(f.fraction * float64(limit-1)) },
	}
}

func (f *fakeClock) total() time.Duration {
	var sum time.Duration
	for _, wait := range f.waited {
		sum += wait
	}
	return sum
}

func throttledUntil(f *fakeClock, refill time.Time, throttle error) func() (string, int, error) {
	return func() (string, int, error) {
		if f.at.Before(refill) {
			return "", AnsweredCode(throttle), throttle
		}
		return "served", http.StatusOK, nil
	}
}

func TestACallThrottledUntilItsPerMinuteQuotaRefillsSucceedsOnceItHas(t *testing.T) {
	t.Parallel()

	for name, throttle := range map[string]error{
		"http 429":                &googleapi.Error{Code: http.StatusTooManyRequests},
		"grpc resource exhausted": status.Error(codes.ResourceExhausted, "Quota exceeded for quota metric 'Write requests' and limit 'Write requests per minute per region'"),
	} {
		for jitter, fraction := range map[string]float64{"longest waits": 1, "half waits": 0.5} {
			t.Run(name+", "+jitter, func(t *testing.T) {
				t.Parallel()
				f := newFakeClock()
				f.fraction = fraction
				refill := f.at.Truncate(time.Minute).Add(time.Minute)

				got, _, err := asked(context.Background(), f.clock(), throttledUntil(f, refill, throttle))
				if err != nil || got != "served" {
					t.Fatalf("asked() = %q, %v after waiting %v, want the call served once the quota refilled %v in", got, err, f.total(), refill.Sub(f.at.Add(-f.total())))
				}
				if f.total() > 2*time.Minute {
					t.Errorf("asked() waited %v for a quota that refilled within a minute, want it asked again within the next window", f.total())
				}
			})
		}
	}
}

func TestACallThrottledThroughoutGivesUpWithGooglesAnswerWithinFiveMinutes(t *testing.T) {
	t.Parallel()
	f := newFakeClock()
	throttle := &googleapi.Error{Code: http.StatusTooManyRequests}

	calls := 0
	_, code, err := asked(context.Background(), f.clock(), func() (string, int, error) {
		calls++
		return "", http.StatusTooManyRequests, throttle
	})
	if code != http.StatusTooManyRequests || !errors.Is(err, throttle) {
		t.Fatalf("asked() = %d, %v, want Google's throttle once the wait ran out", code, err)
	}
	if f.total() < 4*time.Minute || f.total() > 5*time.Minute {
		t.Errorf("asked() waited %v over %d calls, want it to wait out several quota windows but no more than 5m0s", f.total(), calls)
	}
}

func TestAThrottledCallWaitsAsLongAsGoogleAsks(t *testing.T) {
	t.Parallel()

	retryAfter := &googleapi.Error{Code: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"90"}}}
	retryInfo, err := status.New(codes.ResourceExhausted, "quota").WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(45 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	for name, throttled := range map[string]struct {
		throttle error
		delay    time.Duration
	}{
		"Retry-After header": {throttle: retryAfter, delay: 90 * time.Second},
		"RetryInfo detail":   {throttle: retryInfo.Err(), delay: 45 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFakeClock()
			refill := f.at.Add(time.Second)

			if _, _, err := askedOn(f, refill, throttled.throttle); err != nil {
				t.Fatalf("asked() = %v, want the call served after the wait Google asked for", err)
			}
			if len(f.waited) != 1 || f.waited[0] < throttled.delay || f.waited[0] > throttled.delay+throttled.delay/4 {
				t.Errorf("asked() waited %v, want one wait of the %v Google asked for plus at most a quarter of it", f.waited, throttled.delay)
			}
		})
	}
}

func askedOn(f *fakeClock, refill time.Time, throttle error) (string, int, error) {
	return asked(context.Background(), f.clock(), throttledUntil(f, refill, throttle))
}

func TestAThrottledCallStopsWaitingWhenTheRunIsCancelled(t *testing.T) {
	t.Parallel()
	f := newFakeClock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.sleeping = func(waits int) {
		if waits == 3 {
			cancel()
		}
	}

	calls := 0
	_, _, err := asked(ctx, f.clock(), func() (string, int, error) {
		calls++
		return "", http.StatusTooManyRequests, &googleapi.Error{Code: http.StatusTooManyRequests}
	})
	if !errors.Is(err, context.Canceled) || calls != 4 {
		t.Errorf("asked() made %d calls and returned %v, want 4 calls and the run's cancellation", calls, err)
	}
}

func TestAThrottledCallAnswersWithTheThrottleRatherThanWaitPastTheRunsDeadline(t *testing.T) {
	t.Parallel()
	f := newFakeClock()
	ctx, cancel := context.WithDeadline(context.Background(), f.at.Add(10*time.Second))
	defer cancel()
	throttle := &googleapi.Error{Code: http.StatusTooManyRequests}

	_, code, err := asked(ctx, f.clock(), func() (string, int, error) {
		return "", http.StatusTooManyRequests, throttle
	})
	if code != http.StatusTooManyRequests || !errors.Is(err, throttle) {
		t.Errorf("asked() = %d, %v, want Google's throttle, not a wait that outlives the run", code, err)
	}
	if f.total() > 10*time.Second {
		t.Errorf("asked() waited %v, want no wait past the run's deadline 10s away", f.total())
	}
}

func TestAServiceThatStaysUnavailableIsAskedAFewTimesWithinSeconds(t *testing.T) {
	t.Parallel()
	f := newFakeClock()

	calls := 0
	_, code, _ := asked(context.Background(), f.clock(), func() (string, int, error) {
		calls++
		return "", http.StatusServiceUnavailable, &googleapi.Error{Code: http.StatusServiceUnavailable}
	})
	if code != http.StatusServiceUnavailable || calls != Attempts || f.total() > 5*time.Second {
		t.Errorf("asked() made %d calls over %v and answered %d, want %d calls within seconds: a service that is down is not waited on like a quota", calls, f.total(), code, Attempts)
	}
}
