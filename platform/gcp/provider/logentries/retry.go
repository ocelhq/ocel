package logentries

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
	"google.golang.org/api/logging/v2"
)

const (
	retryAttempts = 4
	retryBackoff  = 100 * time.Millisecond
	retryCeiling  = 2 * time.Second
)

var retryableAnswers = []int{
	http.StatusTooManyRequests,
	http.StatusInternalServerError,
	http.StatusBadGateway,
	http.StatusServiceUnavailable,
	http.StatusGatewayTimeout,
}

func listed(ctx context.Context, logs *logging.Service, req *logging.ListLogEntriesRequest) (*logging.ListLogEntriesResponse, error) {
	var (
		page *logging.ListLogEntriesResponse
		err  error
	)
	for attempt := range retryAttempts {
		if attempt > 0 && !waited(ctx, attempt) {
			return nil, ctx.Err()
		}
		if page, err = logs.Entries.List(req).Context(ctx).Do(); !isRetryable(err) {
			return page, err
		}
	}
	return page, err
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	var answered *googleapi.Error
	if errors.As(err, &answered) {
		return slices.Contains(retryableAnswers, answered.Code)
	}
	var timed net.Error
	return (errors.As(err, &timed) && timed.Timeout()) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EPIPE)
}

func waited(ctx context.Context, attempt int) bool {
	backoff := min(retryBackoff<<(attempt-1), retryCeiling)
	timer := time.NewTimer(backoff/2 + rand.N(backoff/2))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
