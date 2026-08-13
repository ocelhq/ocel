package obs

import "time"

// Level is the severity of a log record. It mirrors the handful of levels
// every command's output already distinguishes on screen.
type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// logRecord is the NDJSON shape written to a run's log file: one JSON object
// per line, one line per event. TraceID and, where a span is active, SpanID
// tie a line back to the OTLP trace file that was written alongside it.
type logRecord struct {
	Time    time.Time      `json:"time"`
	Level   Level          `json:"level"`
	Message string         `json:"message"`
	Stage   string         `json:"stage,omitempty"`
	App     string         `json:"app,omitempty"`
	TraceID string         `json:"trace_id"`
	SpanID  string         `json:"span_id,omitempty"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}
