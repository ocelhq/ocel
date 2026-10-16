package task

import (
	"context"
	"fmt"

	"ocel.dev/internal/delivery"
)

type hookFuncs struct {
	onSuccess      any
	onFailure      any
	onComplete     any
	onCancel       any
	catchError     any
	onStartAttempt any
	middleware     delivery.Middleware
}

// OnStartAttempt is called before every attempt of a task, inside the
// worker's middleware. An error it returns fails the attempt.
func OnStartAttempt[P any](fn func(ctx context.Context, payload P) error) Option {
	return option(func(s *settings) { s.hooks.onStartAttempt = fn })
}

// OnSuccess is called once a run has succeeded, with what it returned. An
// error it returns is reported, and does not change the run's outcome.
func OnSuccess[P, R any](fn func(ctx context.Context, payload P, output R) error) Option {
	return option(func(s *settings) { s.hooks.onSuccess = fn })
}

// OnFailure is called once a run has failed for good: aborted, or out of
// attempts. An error it returns is reported, and does not change the run's
// outcome.
func OnFailure[P any](fn func(ctx context.Context, payload P, err error) error) Option {
	return option(func(s *settings) { s.hooks.onFailure = fn })
}

// A RunResult is how a run ended: with Output when Err is nil, or failed with
// Err.
type RunResult[R any] struct {
	// Output is what the run returned.
	Output R
	// Err is why the run failed, or nil when it succeeded.
	Err error
}

// OnComplete is called once a run has ended either way, after [OnSuccess] or
// [OnFailure]. An error it returns is reported, and does not change the run's
// outcome.
func OnComplete[P, R any](fn func(ctx context.Context, payload P, result RunResult[R]) error) Option {
	return option(func(s *settings) { s.hooks.onComplete = fn })
}

// OnCancel is called, on a best-effort basis, when an attempt is cancelled
// while it runs. An error it returns is reported.
func OnCancel[P any](fn func(ctx context.Context, payload P) error) Option {
	return option(func(s *settings) { s.hooks.onCancel = fn })
}

// CatchError is called when an attempt fails with an error other than
// [ocel.dev/runs.ErrAbort], and decides whether the run skips the attempts it
// has left.
func CatchError[P any](fn func(ctx context.Context, payload P, err error) (skipRetrying bool)) Option {
	return option(func(s *settings) { s.hooks.catchError = fn })
}

func mustAssertHookTypes[In, R any](task string, funcs hookFuncs) delivery.Hooks[In, R] {
	hooks := delivery.Hooks[In, R]{
		OnSuccess:      mustAssertHookType[func(context.Context, In, R) error](task, "OnSuccess", funcs.onSuccess),
		OnFailure:      mustAssertHookType[func(context.Context, In, error) error](task, "OnFailure", funcs.onFailure),
		OnCancel:       mustAssertHookType[func(context.Context, In) error](task, "OnCancel", funcs.onCancel),
		CatchError:     mustAssertHookType[func(context.Context, In, error) bool](task, "CatchError", funcs.catchError),
		OnStartAttempt: mustAssertHookType[func(context.Context, In) error](task, "OnStartAttempt", funcs.onStartAttempt),
		Middleware:     funcs.middleware,
	}
	if onComplete := mustAssertHookType[func(context.Context, In, RunResult[R]) error](task, "OnComplete", funcs.onComplete); onComplete != nil {
		hooks.OnComplete = func(ctx context.Context, payload In, output R, err error) error {
			return onComplete(ctx, payload, RunResult[R]{Output: output, Err: err})
		}
	}
	return hooks
}

func mustAssertHookType[F any](task, option string, fn any) F {
	var typed F
	if fn == nil {
		return typed
	}
	typed, ok := fn.(F)
	if !ok {
		panic(fmt.Sprintf("ocel: task %q takes %s a %T, and was given a %T", task, option, typed, fn))
	}
	return typed
}
