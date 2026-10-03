package topics

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"syscall"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	callAttempts = 4
	callBackoff  = 100 * time.Millisecond
	callCeiling  = 2 * time.Second
)

func retried(ctx context.Context, call func() error) error {
	var err error
	for attempt := range callAttempts {
		if attempt > 0 && !waited(ctx, attempt) {
			return ctx.Err()
		}
		if err = call(); !isRetryable(err) {
			return err
		}
	}
	return err
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.ResourceExhausted, codes.DeadlineExceeded, codes.Aborted:
		return true
	}
	switch answeredCode(err) {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	var timed net.Error
	return (errors.As(err, &timed) && timed.Timeout()) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
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

func isAnswered(err error, code int) bool { return answeredCode(err) == code }

func waited(ctx context.Context, attempt int) bool {
	backoff := callBackoff
	for doubled := 1; doubled < attempt && backoff < callCeiling; doubled++ {
		backoff *= 2
	}
	backoff = min(backoff, callCeiling)
	timer := time.NewTimer(backoff/2 + rand.N(backoff/2))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
