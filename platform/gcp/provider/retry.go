package gcp

import (
	"context"
	"errors"
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
	askAttempts = 4
	askBackoff  = 100 * time.Millisecond
	askCeiling  = 2 * time.Second
)

func asked[T any](ctx context.Context, ask func() (T, int, error)) (T, int, error) {
	var (
		value  T
		status int
		err    error
	)
	for attempt := range askAttempts {
		if attempt > 0 && !waited(ctx, attempt) {
			var nothing T
			return nothing, 0, ctx.Err()
		}
		value, status, err = ask()
		if !retryable(status, err) {
			return value, status, err
		}
	}
	return value, status, err
}

func attempted[T any](ctx context.Context, call func(...googleapi.CallOption) (T, error)) (T, error) {
	value, _, err := asked(ctx, func() (T, int, error) {
		held, err := call()
		return held, answeredCode(err), err
	})
	return value, err
}

func dialled[T any](ctx context.Context, call func() (T, error)) (T, error) {
	var (
		value T
		err   error
	)
	for attempt := range askAttempts {
		if attempt > 0 && !waited(ctx, attempt) {
			var nothing T
			return nothing, ctx.Err()
		}
		if value, err = call(); !redialled(err) {
			return value, err
		}
	}
	return value, err
}

func redialled(err error) bool {
	if err == nil {
		return false
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.ResourceExhausted, codes.DeadlineExceeded, codes.Aborted:
		return true
	}
	return retryable(answeredCode(err), err)
}

func done(ctx context.Context, call func() error) error {
	_, err := dialled(ctx, func() (struct{}, error) { return struct{}{}, call() })
	return err
}

var retryableAnswers = []int{
	http.StatusTooManyRequests,
	http.StatusBadGateway,
	http.StatusServiceUnavailable,
	http.StatusGatewayTimeout,
}

func retryable(status int, err error) bool {
	return throttling(status) || (status == 0 && err != nil && transport(err))
}

func throttling(status int) bool { return slices.Contains(retryableAnswers, status) }

func unreachable(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func transport(err error) bool {
	var reached net.Error
	return errors.As(err, &reached) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EPIPE)
}

func answeredCode(err error) int {
	var answered *googleapi.Error
	if errors.As(err, &answered) {
		return answered.Code
	}
	return 0
}

func waited(ctx context.Context, attempt int) bool {
	backoff := min(askBackoff<<(attempt-1), askCeiling)
	timer := time.NewTimer(backoff/2 + rand.N(backoff/2))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
