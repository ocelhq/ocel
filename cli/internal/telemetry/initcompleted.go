package telemetry

import "time"

type InitCompletion struct {
	Language       string
	PackageManager string
	Provider       string
	ConfigFormat   string
}

func NewInitCompleted(identity Identity, at time.Time, completion InitCompletion) (Event, error) {
	return newEvent("init_completed", identity, at, map[string]any{
		"language":        completion.Language,
		"package_manager": completion.PackageManager,
		"provider":        completion.Provider,
		"config_format":   completion.ConfigFormat,
	})
}
