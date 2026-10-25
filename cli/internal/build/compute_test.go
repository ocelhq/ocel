package build

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

var buildingNextContainersFromFunctions = &contractv1.ProviderFacts{BuildsNextContainersFromFunctions: true}

func projectOfApps(t *testing.T, apps ...project.App) *project.Project {
	t.Helper()
	root := t.TempDir()
	for _, a := range apps {
		writeFile(t, filepath.Join(root, a.Path, "package.json"), "{}\n")
	}
	return &project.Project{Dir: root, Slug: "shop", Apps: apps}
}

func namesOf(apps []project.App) []string {
	names := make([]string, 0, len(apps))
	for _, a := range apps {
		names = append(names, a.Name)
	}
	return names
}

func TestANextContainerNoDockerfileBuildsIsBuiltFromItsFunctionOnAProviderThatSaysSo(t *testing.T) {
	cfg := projectOfApps(t, nextContainerApp("web", "web"))

	resolved, err := ResolveNextContainerBuilds(cfg, buildingNextContainersFromFunctions)
	if err != nil {
		t.Fatalf("ResolveNextContainerBuilds() = %v", err)
	}
	if got := namesOf(FunctionApps(resolved.Apps)); !slices.Equal(got, []string{"web"}) {
		t.Errorf("FunctionApps() = %v, want web built by ocel's Next adapter as a serverless Next app is", got)
	}
	if got := namesOf(ImageApps(resolved.Apps)); len(got) != 0 {
		t.Errorf("ImageApps() = %v, want none: no image is built for web on this machine", got)
	}
	if !resolved.Apps[0].RunsOn(provider.ComputeContainer) {
		t.Errorf("web runs on %q, want the container compute it asked for", resolved.Apps[0].Compute)
	}
	if !CanReadVariablesAtBuild(resolved.Apps[0]) {
		t.Error("web reads no value while it builds, and next build inlines its public values as a serverless Next build does")
	}
}

func TestANextContainerADockerfileBuildsKeepsItsImage(t *testing.T) {
	named := nextContainerApp("named", "named")
	named.Container = &project.Container{Build: &project.Build{Dockerfile: "prod.Dockerfile"}}
	cfg := projectOfApps(t, nextContainerApp("beside", "beside"), named)
	writeFile(t, filepath.Join(cfg.Dir, "beside", "Dockerfile"), "FROM scratch\n")
	writeFile(t, filepath.Join(cfg.Dir, "named", "prod.Dockerfile"), "FROM scratch\n")

	resolved, err := ResolveNextContainerBuilds(cfg, buildingNextContainersFromFunctions)
	if err != nil {
		t.Fatalf("ResolveNextContainerBuilds() = %v", err)
	}
	if got := namesOf(ImageApps(resolved.Apps)); !slices.Equal(got, []string{"beside", "named"}) {
		t.Errorf("ImageApps() = %v, want both apps built from their Dockerfiles", got)
	}
}

func TestAContainerKeepsItsImageWhereNoProviderBuildsItFromAFunction(t *testing.T) {
	node := project.App{Name: "api", Path: "api", Compute: provider.ComputeContainer, Framework: buildoutput.FrameworkNode, Container: &project.Container{}}
	for name, run := range map[string]struct {
		facts *contractv1.ProviderFacts
		app   project.App
	}{
		"a Next container on a provider that builds its image alone": {&contractv1.ProviderFacts{}, nextContainerApp("web", "web")},
		"a container that serves no Next":                            {buildingNextContainersFromFunctions, node},
	} {
		t.Run(name, func(t *testing.T) {
			resolved, err := ResolveNextContainerBuilds(projectOfApps(t, run.app), run.facts)
			if err != nil {
				t.Fatalf("ResolveNextContainerBuilds() = %v", err)
			}
			if got := namesOf(ImageApps(resolved.Apps)); !slices.Equal(got, []string{run.app.Name}) {
				t.Errorf("ImageApps() = %v, want %s built as an image", got, run.app.Name)
			}
		})
	}
}

func TestANextContainerBuiltFromItsFunctionRefusesTheImageSettingsOnlyAnImageBuildReads(t *testing.T) {
	for setting, build := range map[string]*project.Build{
		"image.command": {Command: "pnpm build"},
		"image.context": {Context: "."},
	} {
		t.Run(setting, func(t *testing.T) {
			app := nextContainerApp("web", "web")
			app.Container = &project.Container{Build: build}

			_, err := ResolveNextContainerBuilds(projectOfApps(t, app), buildingNextContainersFromFunctions)
			if err == nil || !strings.Contains(err.Error(), setting) || !strings.Contains(err.Error(), "image.dockerfile") {
				t.Fatalf("ResolveNextContainerBuilds() = %v, want %s refused, naming the Dockerfile that would read it", err, setting)
			}
		})
	}
}

func TestTheAppsBuiltForTheirHostAreTheNextAppsNoDockerfileBuilds(t *testing.T) {
	docker := nextContainerApp("docker", "docker")
	node := project.App{Name: "api", Path: "api", Compute: provider.ComputeServerless, Framework: buildoutput.FrameworkNode}
	cfg := projectOfApps(t, nextApp("web", "web"), nextContainerApp("shop", "shop"), docker, node)
	writeFile(t, filepath.Join(cfg.Dir, "docker", "Dockerfile"), "FROM scratch\n")

	got, err := FindAppsBuiltForTheirHost(cfg)
	if err != nil {
		t.Fatalf("FindAppsBuiltForTheirHost() = %v", err)
	}
	if !slices.Equal(got, []string{"web", "shop"}) {
		t.Errorf("FindAppsBuiltForTheirHost() = %v, want the serverless Next app and the Next container no Dockerfile builds", got)
	}
}
