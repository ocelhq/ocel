package gcp

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"time"

	"google.golang.org/api/googleapi"
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

func retryable(status int, err error) bool {
	return throttling(status) || (err != nil && status == 0)
}

func throttling(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
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
