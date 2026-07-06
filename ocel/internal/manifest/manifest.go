// Package manifest holds the in-memory record of resources declared during
// a discovery run, accumulated via ResourceService.Declare.
package manifest

import (
	"sync"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// Entry is a single declared resource.
type Entry struct {
	Name string
	Type resourcesv1.ResourceType
}

// Manifest is a concurrency-safe collection of declared resources.
type Manifest struct {
	mu      sync.Mutex
	entries []Entry
}

// New returns an empty Manifest.
func New() *Manifest {
	return &Manifest{}
}

// Add records a declared resource.
func (m *Manifest) Add(e Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
}

// Snapshot returns a copy of the currently declared resources.
func (m *Manifest) Snapshot() []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Entry, len(m.entries))
	copy(out, m.entries)
	return out
}
