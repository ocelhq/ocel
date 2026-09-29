package build

import (
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/pkg/provider"
)

func ImageApps(apps []projectconfig.App) []projectconfig.App {
	return appsWhere(apps, true)
}

func FunctionApps(apps []projectconfig.App) []projectconfig.App {
	return appsWhere(apps, false)
}

func appsWhere(apps []projectconfig.App, inImage bool) []projectconfig.App {
	var selected []projectconfig.App
	for _, a := range apps {
		if a.RunsOn(provider.ComputeContainer) == inImage {
			selected = append(selected, a)
		}
	}
	return selected
}
