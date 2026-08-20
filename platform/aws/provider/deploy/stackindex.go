package deploy

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/ocelhq/ocel/pkg/naming"
)

type StackIndex interface {
	AddProject(ctx context.Context, project string, features []string) error
	AddStack(ctx context.Context, project string, stack naming.StackName) error
	RemoveStack(ctx context.Context, project string, stack naming.StackName) error
	RemoveProject(ctx context.Context, project string) error
	Stacks(ctx context.Context, project string) ([]naming.StackName, error)
	Projects(ctx context.Context) ([]string, error)
}

var errNoStackIndex = fmt.Errorf("this deploy has no stack index; re-run `ocel bootstrap`")

type Realized struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

func (r *Realized) realize(ctx context.Context, index StackIndex, project string, stack naming.StackName) error {
	if err := index.AddStack(ctx, project, stack); err != nil {
		return err
	}
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.keys == nil {
		r.keys = map[string]struct{}{}
	}
	r.keys[naming.StackKey(project, stack)] = struct{}{}
	return nil
}

func (r *Realized) realizedHere(project string, stack naming.StackName) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.keys[naming.StackKey(project, stack)]
	return ok
}

func stackIndex(index StackIndex) (StackIndex, error) {
	if index == nil {
		return nil, errNoStackIndex
	}
	return index, nil
}

func indexedStacks(ctx context.Context, index StackIndex, slug string) ([]naming.StackName, error) {
	ix, err := stackIndex(index)
	if err != nil {
		return nil, err
	}
	return ix.Stacks(ctx, naming.Sanitize(slug))
}

func ProjectIndexed(ctx context.Context, index StackIndex, slug string) (bool, error) {
	ix, err := stackIndex(index)
	if err != nil {
		return false, err
	}
	projects, err := ix.Projects(ctx)
	if err != nil {
		return false, err
	}
	return slices.Contains(projects, naming.Sanitize(slug)), nil
}

func forgetProjectIfEmpty(ctx context.Context, index StackIndex, slug string) error {
	ix, err := stackIndex(index)
	if err != nil {
		return err
	}
	project := naming.Sanitize(slug)
	remaining, err := ix.Stacks(ctx, project)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return nil
	}
	return ix.RemoveProject(ctx, project)
}
