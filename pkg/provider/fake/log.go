package fake

import (
	"slices"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/progress"
)

type Log struct {
	mu    sync.Mutex
	lines []string
}

var _ progress.Log = (*Log)(nil)

func (p *Log) record(level, message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lines = append(p.lines, level+" "+message)
}

func (p *Log) Say(message string) { p.record("INFO", message) }

func (p *Log) Warn(message string) { p.record("WARN", message) }

func (p *Log) Error(message string) { p.record("ERROR", message) }

func (p *Log) Detail(message string) { p.record("OUTPUT", message) }

func (p *Log) Debug(line string) { p.record("DEBUG", line) }

func (p *Log) Span(string, time.Time, time.Time, error, ...progress.Attr) {}

func (p *Log) Lines() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.lines)
}
