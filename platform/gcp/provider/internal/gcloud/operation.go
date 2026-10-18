package gcloud

import (
	"cmp"
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/refusal"
)

type OperationOutcome struct {
	Name    string
	Done    bool
	Failure string
}

func FailureOf(code int64, message string) string {
	return cmp.Or(message, fmt.Sprintf("the operation failed with code %d and no message", code))
}

func AwaitOperation[T any](
	ctx context.Context,
	limits Patience,
	service, doing string,
	started T,
	outcomeOf func(T) OperationOutcome,
	read func(name string) (T, error),
) (T, error) {
	var nothing T
	first := outcomeOf(started)
	if !first.Done && first.Name == "" {
		return nothing, fmt.Errorf("%s answered %s with an operation that is not done and has no name, so nothing can be polled to say when it ends", service, doing)
	}
	finished, err := Waiting(ctx, limits, doing, func() (T, error) {
		if first.Done {
			return started, nil
		}
		return read(first.Name)
	}, func(polled T) bool { return outcomeOf(polled).Done })
	if err != nil {
		return nothing, err
	}
	if failure := outcomeOf(finished).Failure; failure != "" {
		return nothing, refusal.Refuse(refusal.CodeNotReady, "%s refused %s: %s", service, doing, failure)
	}
	return finished, nil
}
