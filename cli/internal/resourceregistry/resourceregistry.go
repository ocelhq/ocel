package resourceregistry

import (
	"sync"

	"github.com/ocelhq/ocel/cli/internal/declare"
)

type Registry struct {
	mu      sync.Mutex
	entries []declare.Resource
}

func New() *Registry {
	return &Registry{}
}

func (r *Registry) Add(e declare.Resource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
}

func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = nil
}

func (r *Registry) Snapshot() []declare.Resource {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]declare.Resource, len(r.entries))
	copy(out, r.entries)
	return out
}
