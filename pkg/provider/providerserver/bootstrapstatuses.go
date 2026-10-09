package providerserver

import (
	"context"
	"sync"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

type BootstrapStatuses struct {
	mu         sync.Mutex
	read       map[bootstrapStatusKey]BootstrapStatus
	generation int
}

type bootstrapStatusKey struct {
	tier environment.Tier
	edge edge.Kind
}

func (s *BootstrapStatuses) find(key bootstrapStatusKey) (BootstrapStatus, int, bool) {
	if s == nil {
		return BootstrapStatus{}, 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	status, found := s.read[key]
	return status, s.generation, found
}

func (s *BootstrapStatuses) keep(key bootstrapStatusKey, status BootstrapStatus, readAt int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != readAt {
		return
	}
	if s.read == nil {
		s.read = map[bootstrapStatusKey]BootstrapStatus{}
	}
	s.read[key] = status
}

func (s *BootstrapStatuses) forget() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read = nil
	s.generation++
}

func (g Gate) SessionStatus(ctx context.Context, tier environment.Tier) (BootstrapStatus, error) {
	key := bootstrapStatusKey{tier: tier, edge: g.Edge}
	status, readAt, found := g.Statuses.find(key)
	if found {
		return status, nil
	}
	status, err := g.Status(ctx, tier)
	if err != nil {
		return BootstrapStatus{}, err
	}
	g.Statuses.keep(key, status, readAt)
	return status, nil
}
