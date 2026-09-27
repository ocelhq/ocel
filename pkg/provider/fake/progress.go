package fake

import (
	"slices"
	"sync"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Progress struct {
	mu    sync.Mutex
	lines []string
}

var _ edge.Progress = (*Progress)(nil)

func (p *Progress) record(level, message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lines = append(p.lines, level+" "+message)
}

func (p *Progress) Say(message string) { p.record("INFO", message) }

func (p *Progress) Warn(message string) { p.record("WARN", message) }

func (p *Progress) Error(message string) { p.record("ERROR", message) }

func (p *Progress) Detail(message string) { p.record("OUTPUT", message) }

func (p *Progress) Debug(line string) { p.record("DEBUG", line) }

func (p *Progress) Span(string, time.Time, time.Time, error, ...edge.Attr) {}

func (p *Progress) Lines() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.lines)
}
