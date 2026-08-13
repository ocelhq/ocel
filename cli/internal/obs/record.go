package obs

import "time"

type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

type Stage string

type App string

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
