package build

import (
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/build/image"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
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

func ResolveNextContainerBuilds(cfg *project.Project, facts *contractv1.ProviderFacts) (*project.Project, error) {
	if !facts.GetBuildsNextContainersFromFunctions() {
		return cfg, nil
	}
	adapted, err := FindNextAppsNoDockerfileBuilds(cfg)
	if err != nil {
		return nil, err
	}
	resolved := *cfg
	resolved.Apps = slices.Clone(cfg.Apps)
	for at, a := range resolved.Apps {
		if !a.RunsOn(provider.ComputeContainer) || !slices.Contains(adapted, a.Name) {
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

func FindNextAppsNoDockerfileBuilds(cfg *project.Project) ([]string, error) {
	var names []string
	for _, a := range cfg.Apps {
		if a.Framework != buildoutput.FrameworkNext {
			continue
		}
		if a.RunsOn(provider.ComputeContainer) {
			fromDockerfile, err := isBuiltFromDockerfile(cfg, a)
			if err != nil {
				return nil, err
			}
			if fromDockerfile {
				continue
			}
		}
		names = append(names, a.Name)
	}
	return names, nil
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
	return fmt.Errorf("app %q is built by ocel's Next adapter, which reads no %s: drop %s", a.Name, english.And(set), english.Number(len(set), "it", "them"))
}
