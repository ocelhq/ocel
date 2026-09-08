package gcp

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"slices"
	"syscall"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	askAttempts = 4
	askBackoff  = 100 * time.Millisecond
	askCeiling  = 2 * time.Second
)

const (
	waitAttempts = 60
	waitCeiling  = 5 * time.Second
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

func until[T any](ctx context.Context, doing string, ask func() (T, error), settled func(T) bool) (T, error) {
	var nothing T
	for attempt := range waitAttempts {
		if attempt > 0 && !waitedFor(ctx, attempt, waitCeiling) {
			return nothing, fmt.Errorf("wait for %s: %w", doing, ctx.Err())
		}
		value, err := ask()
		if err != nil {
			return nothing, err
		}
		if settled(value) {
			return value, nil
		}
	}
	return nothing, providerkit.Refuse(providerkit.CodeNotReady,
		"%s is still not done after %d attempts, and going on before it is leaves a bootstrap half made.\nTry again once Google has caught up",
		doing, waitAttempts)
}

func waited(ctx context.Context, attempt int) bool { return waitedFor(ctx, attempt, askCeiling) }

func waitedFor(ctx context.Context, attempt int, ceiling time.Duration) bool {
	backoff := min(askBackoff<<(attempt-1), ceiling)
	timer := time.NewTimer(backoff/2 + rand.N(backoff/2))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
