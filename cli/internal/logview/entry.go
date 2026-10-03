package logview

import "time"

type Entry struct {
	Message string
	Level   Level
	Time    time.Time
	Fields  map[string]any
	Error   string
	Raw     string
}
