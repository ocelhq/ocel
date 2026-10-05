package projectinit

import (
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/telemetry"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
)

var telemetryConfigFormats = map[resultv1.ConfigFormat]string{
	resultv1.ConfigFormat_CONFIG_FORMAT_JSON:       "json",
	resultv1.ConfigFormat_CONFIG_FORMAT_YAML:       "yaml",
	resultv1.ConfigFormat_CONFIG_FORMAT_TYPESCRIPT: "ts",
}

func newInitCompletion(configPath, provider, projectDir string, lang sdkLanguage, detected bool) telemetry.InitCompletion {
	completion := telemetry.InitCompletion{Provider: provider, ConfigFormat: telemetryConfigFormats[configFormat(filepath.Base(configPath))]}
	if detected {
		completion.Language = lang.name
		argv, _ := addCommand(projectDir, lang)
		completion.PackageManager = argv[0]
	}
	return completion
}
