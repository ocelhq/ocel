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

func CanReadVariablesAtBuild(a project.App) bool {
	if !a.RunsOn(provider.ComputeServerless) {
		return false
	}
	switch a.Framework() {
	case buildoutput.FrameworkNext, buildoutput.FrameworkRust:
		return true
	}
	return false
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
