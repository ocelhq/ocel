package build

import (
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/provider"
)

func ImageApps(apps []project.App) []project.App {
	return appsWhere(apps, true)
}

func FunctionApps(apps []project.App) []project.App {
	return appsWhere(apps, false)
}

func appsWhere(apps []project.App, inImage bool) []project.App {
	var selected []project.App
	for _, a := range apps {
		if a.RunsOn(provider.ComputeContainer) == inImage {
			selected = append(selected, a)
		}
	}
	return selected
}
