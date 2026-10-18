package bootstrapplan

import (
	"context"
	"sync"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

type Statuses struct {
	mu         sync.Mutex
	read       map[bootstrapStatusKey]Status
	generation int
}

type bootstrapStatusKey struct {
	tier environment.Tier
	edge edge.Kind
}

func (s *Statuses) find(key bootstrapStatusKey) (Status, int, bool) {
	if s == nil {
		return Status{}, 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	status, found := s.read[key]
	return status, s.generation, found
}

func (s *Statuses) keep(key bootstrapStatusKey, status Status, readAt int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != readAt {
		return
	}
	if s.read == nil {
		s.read = map[bootstrapStatusKey]Status{}
	}
	s.read[key] = status
}

func (s *Statuses) Forget() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read = nil
	s.generation++
}

func (r Readiness) SessionStatus(ctx context.Context, tier environment.Tier) (Status, error) {
	key := bootstrapStatusKey{tier: tier, edge: r.Edge}
	status, readAt, found := r.Statuses.find(key)
	if found {
		return status, nil
	}
	status, err := r.Status(ctx, tier)
	if err != nil {
		return Status{}, err
	}
	r.Statuses.keep(key, status, readAt)
	return status, nil
}
