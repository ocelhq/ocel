package build

import (
	"fmt"
	"runtime"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/build/image"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider"
)

func ImageApps(apps []project.App) []project.App {
	return slices.DeleteFunc(slices.Clone(apps), func(a project.App) bool { return !a.IsBuiltAsImage() })
}

func FunctionApps(apps []project.App) []project.App {
	return slices.DeleteFunc(slices.Clone(apps), func(a project.App) bool { return !a.IsBuiltAsFunctions() })
}

func CanReadVariablesAtBuild(a project.App) bool {
	if !a.IsBuiltAsFunctions() {
		return false
	}
	return buildoutput.BuildsWithItsOwnScript(a.Framework) || a.Framework == buildoutput.FrameworkRust
}

func ResolveNextContainerBuilds(cfg *project.Project, host Host) (*project.Project, error) {
	resolved := *cfg
	resolved.Apps = slices.Clone(cfg.Apps)
	if !host.BuildsNextContainersFromFunctions() {
		return &resolved, nil
	}
	for at, a := range resolved.Apps {
		adapted, err := isBuiltByTheAdapter(cfg, a)
		if err != nil {
			return nil, err
		}
		if !adapted {
			continue
		}
		if err := refuseImageSettings(a); err != nil {
			return nil, err
		}
		container := project.Container{}
		if a.Container != nil {
			container = *a.Container
		}
		container.FromFunction = true
		resolved.Apps[at].Container = &container
	}
	return &resolved, nil
}

type targetArch string

func (t targetArch) Architecture() string {
	goarch := string(t)
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	if architecture, known := arch.FromGoArch(goarch); known {
		return architecture
	}
	return goarch
}

func readFunctionArchitecture(a project.App, archs map[string]string) string {
	if !a.IsBuiltFromFunction() {
		return a.Architecture()
	}
	return targetArch(archs[a.Name]).Architecture()
}

func CanBuildWithoutHost(cfg *project.Project) (bool, error) {
	for _, a := range cfg.Apps {
		if a.Framework == buildoutput.FrameworkNext && a.RunsOn(provider.ComputeServerless) {
			return false, nil
		}
		adapted, err := isBuiltByTheAdapter(cfg, a)
		if err != nil || adapted {
			return false, err
		}
	}
	return true, nil
}

func isBuiltByTheAdapter(cfg *project.Project, a project.App) (bool, error) {
	if !a.IsBuiltAsImage() || a.Framework != buildoutput.FrameworkNext {
		return false, nil
	}
	fromDockerfile, err := isBuiltFromDockerfile(cfg, a)
	return !fromDockerfile, err
}

func isBuiltFromDockerfile(cfg *project.Project, a project.App) (bool, error) {
	described, err := image.Describe(cfg, a)
	if err != nil {
		return false, err
	}
	recipe, err := image.ChooseRecipe(described)
	if err != nil {
		return false, err
	}
	return recipe.Dockerfile != "", nil
}

func refuseImageSettings(a project.App) error {
	if a.Container == nil || a.Container.Build == nil {
		return nil
	}
	var set []string
	if a.Container.Build.Command != "" {
		set = append(set, "image.command")
	}
	if a.Container.Build.Context != "" {
		set = append(set, "image.context")
	}
	if len(set) == 0 {
		return nil
	}
	return fmt.Errorf("app %q is built by ocel's Next adapter, which reads no %s: drop %s", a.Name, english.Or(set), english.Number(len(set), set[0], "them"))
}
