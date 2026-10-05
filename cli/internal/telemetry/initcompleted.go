package telemetry

type InitCompletion struct {
	Language       string
	PackageManager string
	Provider       string
	ConfigFormat   string
}

func (InitCompletion) name() string { return "init_completed" }

func (completion InitCompletion) properties() map[string]any {
	return map[string]any{
		"language":        completion.Language,
		"package_manager": completion.PackageManager,
		"provider":        completion.Provider,
		"config_format":   completion.ConfigFormat,
	}
}
