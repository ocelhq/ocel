package providerserver

import (
	"context"
	"sync"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

type BootstrapStatuses struct {
	mu   sync.Mutex
	read map[bootstrapStatusKey]BootstrapStatus
}

type bootstrapStatusKey struct {
	tier environment.Tier
	edge edge.Kind
}

func (s *BootstrapStatuses) find(key bootstrapStatusKey) (BootstrapStatus, bool) {
	if s == nil {
		return BootstrapStatus{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	status, found := s.read[key]
	return status, found
}

func (s *BootstrapStatuses) keep(key bootstrapStatusKey, status BootstrapStatus) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
}

func (g Gate) SessionStatus(ctx context.Context, tier environment.Tier) (BootstrapStatus, error) {
	key := bootstrapStatusKey{tier: tier, edge: g.Edge}
	if status, found := g.Statuses.find(key); found {
		return status, nil
	}
	status, err := g.Status(ctx, tier)
	if err != nil {
		return BootstrapStatus{}, err
	}
	g.Statuses.keep(key, status)
	return status, nil
}
