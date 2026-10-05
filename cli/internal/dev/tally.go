package dev

import (
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
)

type tally struct {
	record  func(telemetry.Payload)
	started time.Time

	mu      sync.Mutex
	reloads int
	kinds   []string
	codes   map[string]int
}

func newTally(record func(telemetry.Payload)) *tally {
	return &tally{record: record, started: time.Now(), codes: map[string]int{}}
}

func (t *tally) noteReload() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reloads++
}

func (t *tally) noteKinds(kinds []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.kinds = kinds
}

func (t *tally) noteError(err error) {
	if err == nil {
		return
	}
	code := clierror.NewRunError(err).GetCode()
	if code == clierror.CodeInterrupted || !slices.Contains(clierror.Codes(), code) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.codes[code]++
}

func (t *tally) end(final error) {
	if t.record == nil {
		return
	}
	t.noteError(final)
	t.mu.Lock()
	session := telemetry.DevSession{Duration: time.Since(t.started), Reloads: t.reloads, ResourceKinds: slices.Clone(t.kinds), ErrorCodes: maps.Clone(t.codes)}
	t.mu.Unlock()
	t.record(session)
}
