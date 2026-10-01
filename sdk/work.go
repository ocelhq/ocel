package ocel

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// ErrAbort fails a run or message without retrying it. Return it, or an error
// wrapping it, from a task's run or a consumer's handler:
//
//	return fmt.Errorf("image %s is not a png: %w", key, ocel.ErrAbort)
var ErrAbort = errors.New("aborted without retrying")

type work[In, R any] struct {
	kind  RunKind
	name  string
	run   func(context.Context, In) (R, error)
	hooks taskHooks[In, R]
}

func (w work[In, R]) serve(ctx context.Context, payload In, wrap middleware) answer {
	output, err := w.runAttempt(ctx, payload, wrap)

	var body []byte
	if err == nil {
		body, err = encodeJSON(output)
		if err != nil {
			err = fmt.Errorf("the output does not encode as JSON: %w: %w", err, ErrAbort)
		}
	}

	cancelled := ctx.Err() != nil
	if cancelled && w.hooks.onCancel != nil {
		w.reportHookError("OnCancel", w.hooks.onCancel(context.WithoutCancel(ctx), payload))
	}

	if err == nil {
		if !cancelled {
			w.completeSuccess(ctx, payload, output)
		}
		return newSuccessAnswer(body)
	}

	abort := errors.Is(err, ErrAbort)
	if !abort && w.hooks.catchError != nil {
		abort = w.hooks.catchError(ctx, payload, err)
	}
	run, _ := RunFrom(ctx)
	if !cancelled && (abort || run.Attempt.Number >= run.Attempt.Of) {
		w.completeFailure(ctx, payload, err)
	}
	if abort {
		return newAbortAnswer(err.Error())
	}
	return newRetryAnswer(err)
}

func (w work[In, R]) runAttempt(ctx context.Context, payload In, wrap middleware) (R, error) {
	var output R
	err := catchPanic(func() error {
		return wrap(ctx, func(ctx context.Context) error {
			if w.hooks.onStartAttempt != nil {
				if err := w.hooks.onStartAttempt(ctx, payload); err != nil {
					return err
				}
			}
			run := func(ctx context.Context) error {
				var err error
				output, err = w.run(ctx, payload)
				return err
			}
			if w.hooks.middleware != nil {
				return w.hooks.middleware(ctx, run)
			}
			return run(ctx)
		})
	})
	return output, err
}

func (w work[In, R]) completeSuccess(ctx context.Context, payload In, output R) {
	if w.hooks.onSuccess != nil {
		w.reportHookError("OnSuccess", w.hooks.onSuccess(ctx, payload, output))
	}
	if w.hooks.onComplete != nil {
		w.reportHookError("OnComplete", w.hooks.onComplete(ctx, payload, RunResult[R]{Output: output}))
	}
}

func (w work[In, R]) completeFailure(ctx context.Context, payload In, err error) {
	if w.hooks.onFailure != nil {
		w.reportHookError("OnFailure", w.hooks.onFailure(ctx, payload, err))
	}
	if w.hooks.onComplete != nil {
		w.reportHookError("OnComplete", w.hooks.onComplete(ctx, payload, RunResult[R]{Err: err}))
	}
}

func (w work[In, R]) reportHookError(hook string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: %s %q: %s: %v\n", w.kind, w.name, hook, err)
	}
}

func catchPanic(fn func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	return fn()
}
