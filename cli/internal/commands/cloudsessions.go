package commands

import (
	"sync"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
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
}

func NewCloudSessions() *CloudSessions {
	return &CloudSessions{open: map[cloudSession]*openCloudSession{}}
}

func (s *CloudSessions) find(r *run.Run, cfg *project.Project) *openCloudSession {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open[cloudSession{run: r, project: cfg}]
}

func (s *CloudSessions) keep(r *run.Run, cfg *project.Project, session *openCloudSession) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.open[cloudSession{run: r, project: cfg}] = session
}
