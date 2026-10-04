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

func groupTraces(entries []*contractv1.LogEntry) [][]*contractv1.LogEntry {
	var groups [][]*contractv1.LogEntry
	for start := 0; start < len(entries); {
		end := start + 1
		for end < len(entries) && originOf(entries[end]) == originOf(entries[start]) {
			end++
		}
		run := entries[start:end]
		messages := make([]string, len(run))
		for i, entry := range run {
			messages[i] = entry.GetMessage()
		}
		for _, joined := range logview.Join(messages) {
			groups = append(groups, run[:len(joined)])
			run = run[len(joined):]
		}
		start = end
	}
	return groups
}
