package commands

import (
	"sync"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
)

type CloudSessions struct {
	mu   sync.Mutex
	open map[cloudSession]*providerprocess.Provider
}

type cloudSession struct {
	run     *run.Run
	project *project.Project
}

func NewCloudSessions() *CloudSessions {
	return &CloudSessions{open: map[cloudSession]*providerprocess.Provider{}}
}

func (s *CloudSessions) find(r *run.Run, cfg *project.Project) *providerprocess.Provider {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open[cloudSession{run: r, project: cfg}]
}

func (s *CloudSessions) keep(r *run.Run, cfg *project.Project, provider *providerprocess.Provider) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.open[cloudSession{run: r, project: cfg}] = provider
}
