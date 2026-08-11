package deploy

import (
	"context"
	"fmt"
	"sync"
)

type StackIndex interface {
	AddProject(ctx context.Context, scope string) error
	AddStack(ctx context.Context, stackName string) error
	RemoveStack(ctx context.Context, stackName string) error
	RemoveProject(ctx context.Context, scope string) error
	Stacks(ctx context.Context, scope string) ([]string, error)
	Projects(ctx context.Context) ([]string, error)
}

var errNoStackIndex = fmt.Errorf("this deploy has no stack index; re-run `ocel bootstrap`")

type realizedStacks struct {
	mu    sync.Mutex
	names map[string]struct{}
}

func (r *realizedStacks) realize(ctx context.Context, index StackIndex, stackName string) error {
	if err := index.AddStack(ctx, stackName); err != nil {
		return err
	}
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.names == nil {
		r.names = map[string]struct{}{}
	}
	r.names[stackName] = struct{}{}
	return nil
}

func (r *realizedStacks) realizedHere(stackName string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.names[stackName]
	return ok
}

func stackIndex(index StackIndex) (StackIndex, error) {
	if index == nil {
		return nil, errNoStackIndex
	}
	return index, nil
}

func indexedStacks(ctx context.Context, index StackIndex, slug string) ([]string, error) {
	ix, err := stackIndex(index)
	if err != nil {
		return nil, err
	}
	return ix.Stacks(ctx, safeName(slug))
}

func forgetProjectIfEmpty(ctx context.Context, index StackIndex, slug string) error {
	ix, err := stackIndex(index)
	if err != nil {
		return err
	}
	scope := safeName(slug)
	remaining, err := ix.Stacks(ctx, scope)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return nil
	}
	return ix.RemoveProject(ctx, scope)
}
