package googleretry

import (
	"context"
	"net/http"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	servedAttempts = 20
	servedCeiling  = 5 * time.Second
)

func IsUnserved(err error) bool {
	return AnsweredCode(err) == http.StatusNotFound || status.Code(err) == codes.NotFound
}

func UntilServed(ctx context.Context, call func() error) error {
	var err error
	for attempt := range servedAttempts {
		if attempt > 0 && !WaitedFor(ctx, attempt, servedCeiling) {
			return ctx.Err()
		}
		if err = call(); !IsUnserved(err) {
			return err
		}
	}
	return err
}

func UntilServedWithin(ctx context.Context, patience time.Duration, call func() error) error {
	deadline := time.Now().Add(patience)
	err := call()
	for attempt := 1; IsUnserved(err) && time.Now().Before(deadline); attempt++ {
		if !WaitedFor(ctx, attempt, servedCeiling) {
			return ctx.Err()
		}
		err = call()
	}
	return err
}

func AttemptedUntilServed[T any](ctx context.Context, call func(...googleapi.CallOption) (T, error)) (T, error) {
	var value T
	err := UntilServed(ctx, func() error {
		var err error
		value, err = Attempted(ctx, call)
		return err
	})
	return value, err
}
