package telemetry

import (
	"maps"
	"slices"
	"time"
)

type DevSession struct {
	Stack
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
	properties := session.stackProperties()
	maps.Copy(properties, map[string]any{
		"duration_ms":    session.Duration.Milliseconds(),
		"reloads":        session.Reloads,
		"resource_kinds": kinds,
		"error_codes":    codes,
	})
	return properties
}
