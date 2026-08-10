package manifest

import (
	"sync"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

type Entry struct {
	Name string
	Type resourcesv1.ResourceType
}

type Manifest struct {
	mu      sync.Mutex
	entries []Entry
}

func New() *Manifest {
	return &Manifest{}
}

func (m *Manifest) Add(e Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
}

func (m *Manifest) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = nil
}

func (m *Manifest) Snapshot() []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Entry, len(m.entries))
	copy(out, m.entries)
	return out
}
