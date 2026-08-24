package deploy

import (
	"context"
	"sync"

	"github.com/ocelhq/ocel/pkg/naming"
)

type TagSweeper interface {
	SweepTagClock(ctx context.Context, project string, stack naming.StackName) error
}

type Realized struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

func (r *Realized) mark(project string, stack naming.StackName) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.keys == nil {
		r.keys = map[string]struct{}{}
	}
	r.keys[naming.StackKey(project, stack)] = struct{}{}
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
