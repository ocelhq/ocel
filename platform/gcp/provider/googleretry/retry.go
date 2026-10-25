package googleretry

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"slices"
	"syscall"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	Attempts = 4
	backoff  = 100 * time.Millisecond
	ceiling  = 2 * time.Second
)

var retryableAnswers = []int{
	http.StatusRequestTimeout,
	http.StatusTooManyRequests,
	http.StatusInternalServerError,
	http.StatusBadGateway,
	http.StatusServiceUnavailable,
	http.StatusGatewayTimeout,
}

var retryableStatuses = []codes.Code{
	codes.Unavailable,
	codes.ResourceExhausted,
	codes.DeadlineExceeded,
	codes.Aborted,
}

type clock struct {
	now    func() time.Time
	sleep  func(ctx context.Context, wait time.Duration) bool
	random func(limit time.Duration) time.Duration
}

var wallClock = clock{now: time.Now, sleep: slept, random: rand.N[time.Duration]}

func Asked[T any](ctx context.Context, ask func() (T, int, error)) (T, int, error) {
	return asked(ctx, wallClock, ask)
}

func asked[T any](ctx context.Context, c clock, ask func() (T, int, error)) (T, int, error) {
	started := c.now()
	for failures, throttles := 0, 0; ; {
		value, code, err := ask()
		var wait time.Duration
		switch {
		case isThrottled(code, err):
			wait = c.throttleWait(err, throttles)
			throttles++
			if c.now().Sub(started)+wait > throttleBudget {
				return value, code, err
			}
		case isRetryable(code, err):
			failures++
			if failures >= Attempts {
				return value, code, err
			}
			wait = c.backoff(failures, ceiling)
		default:
			return value, code, err
		}
		if ctx.Err() == nil && c.isPastDeadline(ctx, wait) {
			return value, code, err
		}
		if !c.sleep(ctx, wait) {
			var nothing T
			return nothing, 0, ctx.Err()
		}
	}
}

func (c clock) isPastDeadline(ctx context.Context, wait time.Duration) bool {
	deadline, set := ctx.Deadline()
	return set && c.now().Add(wait).After(deadline)
}

func Called[T any](ctx context.Context, call func() (T, error)) (T, error) {
	value, _, err := Asked(ctx, func() (T, int, error) {
		value, err := call()
		return value, AnsweredCode(err), err
	})
	return value, err
}

func Attempted[T any](ctx context.Context, call func(...googleapi.CallOption) (T, error)) (T, error) {
	return Called(ctx, func() (T, error) { return call() })
}

func Done(ctx context.Context, call func() error) error {
	_, err := Called(ctx, func() (struct{}, error) { return struct{}{}, call() })
	return err
}

func AnsweredCode(err error) int {
	var answered *googleapi.Error
	if errors.As(err, &answered) {
		return answered.Code
	}
	return 0
}

func isRetryable(code int, err error) bool {
	if code != 0 {
		return IsRetryableAnswer(code)
	}
	return err != nil && (slices.Contains(retryableStatuses, status.Code(err)) || isRetryableTransport(err))
}

func IsRetryableAnswer(code int) bool { return slices.Contains(retryableAnswers, code) }

func isRetryableTransport(err error) bool {
	var timed net.Error
	return (errors.As(err, &timed) && timed.Timeout()) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EPIPE)
}

func Waited(ctx context.Context, attempt int) bool { return WaitedFor(ctx, attempt, ceiling) }

func WaitedFor(ctx context.Context, attempt int, limit time.Duration) bool {
	return slept(ctx, wallClock.backoff(attempt, limit))
}

func (c clock) backoff(attempt int, limit time.Duration) time.Duration {
	wait := backoff
	for doubled := 1; doubled < attempt && wait < limit; doubled++ {
		wait *= 2
	}
	wait = min(wait, limit)
	return wait/2 + c.random(wait/2)
}

func slept(ctx context.Context, wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func IsStale(err error) bool {
	switch AnsweredCode(err) {
	case http.StatusConflict, http.StatusPreconditionFailed:
		return true
	}
	return status.Code(err) == codes.Aborted || status.Code(err) == codes.FailedPrecondition
}
