package commands

import (
	"slices"
	"sync"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type CloudSessions struct {
	mu   sync.Mutex
	open map[cloudSession]*openCloudSession
}

type cloudSession struct {
	run     *run.Run
	project *project.Project
}

type openCloudSession struct {
	provider *providerprocess.Provider
	identity *contractv1.Identity
	scopes   []cloudv1.Scope
}

func NewCloudSessions() *CloudSessions {
	return &CloudSessions{open: map[cloudSession]*openCloudSession{}}
}

func (s *CloudSessions) find(r *run.Run, cfg *project.Project, scopes []cloudv1.Scope) *openCloudSession {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	open := s.open[cloudSession{run: r, project: cfg}]
	if open == nil || slices.ContainsFunc(scopes, func(scope cloudv1.Scope) bool { return !slices.Contains(open.scopes, scope) }) {
		return nil
	}
	return open
}

func (s *CloudSessions) keep(r *run.Run, cfg *project.Project, session *openCloudSession) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.open[cloudSession{run: r, project: cfg}] = session
}
