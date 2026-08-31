package fake

import (
	"slices"
	"sync"
)

type Journal struct {
	mu      sync.Mutex
	entries []string
}

func (j *Journal) note(entry string) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries = append(j.entries, entry)
}

func (j *Journal) Entries() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return slices.Clone(j.entries)
}
