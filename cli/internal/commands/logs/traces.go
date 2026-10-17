package logs

import (
	"github.com/ocelhq/ocel/cli/internal/logview"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type lineOrigin struct {
	app, source, release, instance string
	stream                         contractv1.LogStream
}

func originOf(entry *contractv1.LogEntry) lineOrigin {
	return lineOrigin{entry.GetApp(), entry.GetSource(), entry.GetRelease(), entry.GetInstance(), entry.GetStream()}
}

type openGroup struct {
	origin  lineOrigin
	lines   logview.LineStream
	entries []*contractv1.LogEntry
}

func (g *openGroup) add(entry *contractv1.LogEntry) (closed []*contractv1.LogEntry) {
	origin := originOf(entry)
	sameOrigin := origin == g.origin
	if !sameOrigin {
		g.lines = logview.LineStream{}
	}
	if g.lines.Add(entry.GetMessage()) && sameOrigin && len(g.entries) > 0 {
		g.entries = append(g.entries, entry)
		return nil
	}
	closed = g.entries
	g.origin, g.entries = origin, []*contractv1.LogEntry{entry}
	return closed
}

func (g *openGroup) take() []*contractv1.LogEntry {
	closed := g.entries
	*g = openGroup{}
	return closed
}
