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

func Asked[T any](ctx context.Context, ask func() (T, int, error)) (T, int, error) {
	var (
		value T
		code  int
		err   error
	)
	for attempt := range Attempts {
		if attempt > 0 && !Waited(ctx, attempt) {
			var nothing T
			return nothing, 0, ctx.Err()
		}
		value, code, err = ask()
		if !isRetryable(code, err) {
			return value, code, err
		}
	}
	return value, code, err
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
	wait := backoff
	for doubled := 1; doubled < attempt && wait < limit; doubled++ {
		wait *= 2
	}
	wait = min(wait, limit)
	timer := time.NewTimer(wait/2 + rand.N(wait/2))
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
