package ocel

import (
	"context"
	"fmt"
)

type hookFuncs struct {
	onSuccess      any
	onFailure      any
	onComplete     any
	onCancel       any
	catchError     any
	onStartAttempt any
	middleware     middleware
}

type taskHooks[In, R any] struct {
	onSuccess      func(context.Context, In, R) error
	onFailure      func(context.Context, In, error) error
	onComplete     func(context.Context, In, RunResult[R]) error
	onCancel       func(context.Context, In) error
	catchError     func(context.Context, In, error) bool
	onStartAttempt func(context.Context, In) error
	middleware     middleware
}

func mustAssertHookTypes[In, R any](task string, funcs hookFuncs) taskHooks[In, R] {
	return taskHooks[In, R]{
		onSuccess:      mustAssertHookType[func(context.Context, In, R) error](task, "OnSuccess", funcs.onSuccess),
		onFailure:      mustAssertHookType[func(context.Context, In, error) error](task, "OnFailure", funcs.onFailure),
		onComplete:     mustAssertHookType[func(context.Context, In, RunResult[R]) error](task, "OnComplete", funcs.onComplete),
		onCancel:       mustAssertHookType[func(context.Context, In) error](task, "OnCancel", funcs.onCancel),
		catchError:     mustAssertHookType[func(context.Context, In, error) bool](task, "CatchError", funcs.catchError),
		onStartAttempt: mustAssertHookType[func(context.Context, In) error](task, "OnStartAttempt", funcs.onStartAttempt),
		middleware:     funcs.middleware,
	}
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
