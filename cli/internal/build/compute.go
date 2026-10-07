package build

import (
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider"
)

func ImageApps(apps []project.App) []project.App {
	return appsOn(apps, provider.ComputeContainer)
}

func FunctionApps(apps []project.App) []project.App {
	return appsOn(apps, provider.ComputeServerless)
}

func IsNextFunction(a project.App) bool {
	return a.RunsOn(provider.ComputeServerless) && a.Framework() == buildoutput.FrameworkNext
}

func appsOn(apps []project.App, compute provider.Compute) []project.App {
	var selected []project.App
	for _, a := range apps {
		if a.RunsOn(compute) {
			selected = append(selected, a)
		}
	}
	return selected
}
