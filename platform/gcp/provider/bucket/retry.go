package bucket

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"time"

	"google.golang.org/api/googleapi"
)

func answeredStatus(err error) int {
	var answered *googleapi.Error
	if errors.As(err, &answered) {
		return answered.Code
	}
	return 0
}

const (
	askAttempts = 4
	askBackoff  = 100 * time.Millisecond
	askCeiling  = 2 * time.Second
)

func retried[T any](ctx context.Context, ask func() (T, int, error)) (T, error) {
	var (
		value  T
		status int
		err    error
	)
	for attempt := range askAttempts {
		if attempt > 0 && !waited(ctx, attempt) {
			var nothing T
			return nothing, ctx.Err()
		}
		value, status, err = ask()
		if err == nil || !retryable(ctx, status, err) {
			return value, err
		}
	}
	return value, err
}

func retryable(ctx context.Context, status int, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if status == 0 {
		return networkFailed(err)
	}
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func networkFailed(err error) bool {
	var failed *net.OpError
	var timed interface{ Timeout() bool }
	return errors.As(err, &failed) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) ||
		(errors.As(err, &timed) && timed.Timeout())
}

func waited(ctx context.Context, attempt int) bool {
	backoff := min(askBackoff<<(attempt-1), askCeiling)
	timer := time.NewTimer(backoff/2 + rand.N(backoff/2+1))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
