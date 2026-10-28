package runtrace

import (
	"path/filepath"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/buildoutput"
)

const (
	RunsDirName    = "runs"
	EventsFileName = "events.ndjson"
	TraceFileName  = "trace.otlp.json"
)

const (
	runStartLayout       = "2006-01-02T15-04-05Z"
	commandNameBytes     = 32
	traceIDNameHexDigits = 8
)

type runDirectory struct {
	path     string
	modified time.Time
}

func RunsDir(projectDir string) string {
	return filepath.Join(projectDir, buildoutput.StateDir, RunsDirName)
}

func runDirectoryName(start time.Time, command string, id trace.TraceID) string {
	return start.UTC().Format(runStartLayout) + "-" + commandName(command) + "-" + id.String()[:traceIDNameHexDigits]
}

func commandName(command string) string {
	var name strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimPrefix(command, "ocel ")) {
		switch {
		case 'a' <= r && r <= 'z' || '0' <= r && r <= '9':
			if dash && name.Len() > 0 {
				name.WriteByte('-')
			}
			name.WriteRune(r)
			dash = false
		default:
			dash = true
		}
	}
	trimmed := strings.TrimRight(name.String()[:min(name.Len(), commandNameBytes)], "-")
	if trimmed == "" {
		return "run"
	}
	return trimmed
}
