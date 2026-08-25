package resourceregistry

import (
	"sync"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
)

type Entry struct {
	Name string
	Type linksv1.LinkType
}

type Registry struct {
	mu      sync.Mutex
	entries []Entry
}

func New() *Registry {
	return &Registry{}
}

func (r *Registry) Add(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
}

func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = nil
}

func (r *Registry) Snapshot() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, len(r.entries))
	copy(out, r.entries)
	return out
}
