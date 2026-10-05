package telemetry

import (
	"slices"
	"time"
)

type DevSession struct {
	Duration      time.Duration
	Reloads       int
	ResourceKinds []string
	ErrorCodes    map[string]int
}

func (DevSession) name() string { return "dev_session_ended" }

func (session DevSession) properties() map[string]any {
	kinds := append([]string{}, session.ResourceKinds...)
	slices.Sort(kinds)
	codes := session.ErrorCodes
	if codes == nil {
		codes = map[string]int{}
	}
	return map[string]any{
		"duration_ms":    session.Duration.Milliseconds(),
		"reloads":        session.Reloads,
		"resource_kinds": kinds,
		"error_codes":    codes,
	}
}
