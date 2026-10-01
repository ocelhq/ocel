package gcp

import (
	"cmp"
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/refusal"
)

type operationOutcome struct {
	name    string
	done    bool
	failure string
}

func failureOf(code int64, message string) string {
	return cmp.Or(message, fmt.Sprintf("the operation failed with code %d and no message", code))
}

func awaitOperation[T any](
	ctx context.Context,
	limits patience,
	service, doing string,
	started T,
	outcomeOf func(T) operationOutcome,
	read func(name string) (T, error),
) (T, error) {
	var nothing T
	first := outcomeOf(started)
	if !first.done && first.name == "" {
		return nothing, fmt.Errorf("%s answered %s with an operation that is not done and has no name, so nothing can be polled to say when it ends", service, doing)
	}
	finished, err := waiting(ctx, limits, doing, func() (T, error) {
		if first.done {
			return started, nil
		}
		return read(first.name)
	}, func(polled T) bool { return outcomeOf(polled).done })
	if err != nil {
		return nothing, err
	}
	if failure := outcomeOf(finished).failure; failure != "" {
		return nothing, refusal.Refuse(refusal.CodeNotReady, "%s refused %s: %s", service, doing, failure)
	}
	return finished, nil
}
