package cli

import (
	"slices"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func projectFrameworks(cfg *projectconfig.Config) []string {
	var frameworks []string
	for _, app := range cfg.Apps {
		if app.Framework != "" {
			frameworks = append(frameworks, app.Framework)
		}
	}
	slices.Sort(frameworks)
	return slices.Compact(frameworks)
}
