package preflight

import (
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
)

func TestOnlyContainerAppsAreNamedEachWithTheArchitectureItDeclares(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Apps: []project.App{
		{Name: "web", Compute: "container", Framework: project.Framework{Arch: "arm64"}},
		{Name: "worker", Compute: "container"},
		{Name: "api", Compute: "serverless", Framework: project.Framework{Name: "node", Arch: "arm64"}},
		{Name: "site"},
	}}

	named := Containers(cfg)
	if len(named) != 2 {
		t.Fatalf("Containers() = %v, want web and worker alone: nothing else has an image to build", named)
	}
	if named[0].GetApp() != "web" || named[0].GetArch() != "arm64" {
		t.Errorf("Containers()[0] = %v, want web declaring arm64", named[0])
	}
	if named[1].GetApp() != "worker" || named[1].GetArch() != "" {
		t.Errorf("Containers()[1] = %v, want worker declaring nothing, so the provider names what it runs on", named[1])
	}
}
