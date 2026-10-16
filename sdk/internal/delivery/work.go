package delivery

import (
	"context"
	"errors"
	"fmt"
	"os"

	"ocel.dev/internal/binding"
	"ocel.dev/runs"
)

type Work[In, R any] struct {
	Kind  runs.Kind
	Name  string
	Run   func(context.Context, In) (R, error)
	Hooks Hooks[In, R]
}

type Hooks[In, R any] struct {
	OnSuccess      func(context.Context, In, R) error
	OnFailure      func(context.Context, In, error) error
	OnComplete     func(context.Context, In, R, error) error
	OnCancel       func(context.Context, In) error
	CatchError     func(context.Context, In, error) bool
	OnStartAttempt func(context.Context, In) error
	Middleware     Middleware
}

func (w Work[In, R]) serve(ctx context.Context, payload In, wrap Middleware) answer {
	output, err := w.runAttempt(ctx, payload, wrap)

	var body []byte
	if err == nil {
		body, err = binding.EncodeJSON(output)
		if err != nil {
			err = fmt.Errorf("the output does not encode as JSON: %w: %w", err, runs.ErrAbort)
		}
	}

	cancelled := ctx.Err() != nil
	if cancelled && w.Hooks.OnCancel != nil {
		w.reportHookError("OnCancel", w.Hooks.OnCancel(context.WithoutCancel(ctx), payload))
	}

	if err == nil {
		if !cancelled {
			w.completeSuccess(ctx, payload, output)
		}
		return newSuccessAnswer(body)
	}

	abort := errors.Is(err, runs.ErrAbort)
	if !abort && w.Hooks.CatchError != nil {
		abort = w.Hooks.CatchError(ctx, payload, err)
	}
	run, _ := runs.FromContext(ctx)
	if !cancelled && (abort || run.Attempt.Number >= run.Attempt.Of) {
		w.completeFailure(ctx, payload, err)
	}
	if abort {
		return newAbortAnswer(err.Error())
	}
	return newRetryAnswer(err)
}

func (w Work[In, R]) runAttempt(ctx context.Context, payload In, wrap Middleware) (R, error) {
	var output R
	err := catchPanic(func() error {
		return wrap(ctx, func(ctx context.Context) error {
			if w.Hooks.OnStartAttempt != nil {
				if err := w.Hooks.OnStartAttempt(ctx, payload); err != nil {
					return err
				}
			}
			run := func(ctx context.Context) error {
				var err error
				output, err = w.Run(ctx, payload)
				return err
			}
			if w.Hooks.Middleware != nil {
				return w.Hooks.Middleware(ctx, run)
			}
			return run(ctx)
		})
	})
	return output, err
}

func (w Work[In, R]) completeSuccess(ctx context.Context, payload In, output R) {
	if w.Hooks.OnSuccess != nil {
		w.reportHookError("OnSuccess", w.Hooks.OnSuccess(ctx, payload, output))
	}
	if w.Hooks.OnComplete != nil {
		w.reportHookError("OnComplete", w.Hooks.OnComplete(ctx, payload, output, nil))
	}
}

func (w Work[In, R]) completeFailure(ctx context.Context, payload In, err error) {
	if w.Hooks.OnFailure != nil {
		w.reportHookError("OnFailure", w.Hooks.OnFailure(ctx, payload, err))
	}
	if w.Hooks.OnComplete != nil {
		var zero R
		w.reportHookError("OnComplete", w.Hooks.OnComplete(ctx, payload, zero, err))
	}
}

func (w Work[In, R]) reportHookError(hook string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: %s %q: %s: %v\n", w.Kind, w.Name, hook, err)
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
