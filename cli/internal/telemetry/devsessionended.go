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

func NewDevSessionEnded(identity Identity, at time.Time, session DevSession) (Event, error) {
	kinds := append([]string{}, session.ResourceKinds...)
	slices.Sort(kinds)
	codes := session.ErrorCodes
	if codes == nil {
		codes = map[string]int{}
	}
	return newEvent("dev_session_ended", identity, at, map[string]any{
		"duration_ms":    session.Duration.Milliseconds(),
		"reloads":        session.Reloads,
		"resource_kinds": kinds,
		"error_codes":    codes,
	})
}
