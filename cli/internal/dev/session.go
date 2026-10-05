package dev

import (
	"slices"
	"sync"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
)

type session struct {
	record  func(telemetry.DevSession)
	started time.Time

	mu      sync.Mutex
	reloads int
	kinds   []string
	codes   map[string]int
}

func newSession(record func(telemetry.DevSession)) *session {
	return &session{record: record, started: time.Now(), codes: map[string]int{}}
}

func (s *session) noteReload() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloads++
}

func (s *session) noteKinds(kinds []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kinds = kinds
}

func (s *session) noteError(err error) {
	if err == nil {
		return
	}
	code := clierror.NewRunError(err).GetCode()
	if code == clierror.CodeInterrupted || !slices.Contains(clierror.Codes(), code) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[code]++
}

func (s *session) end(final error) {
	if s.record == nil {
		return
	}
	s.noteError(final)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record(telemetry.DevSession{Duration: time.Since(s.started), Reloads: s.reloads, ResourceKinds: s.kinds, ErrorCodes: s.codes})
}
