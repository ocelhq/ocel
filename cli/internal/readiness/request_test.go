package readiness

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
)

func TestOnlyContainerAppsAreNamedEachWithTheArchitectureItDeclares(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Apps: []project.App{
		{Name: "web", Compute: "container", Arch: "arm64"},
		{Name: "worker", Compute: "container"},
		{Name: "api", Compute: "serverless", Serverless: &project.Serverless{Framework: "node"}, Arch: "arm64"},
		{Name: "site"},
	}}

	named := containers(cfg)
	if len(named) != 2 {
		t.Fatalf("containers() = %v, want web and worker alone: nothing else has an image to build", named)
	}
	if named[0].GetApp() != "web" || named[0].GetArch() != "arm64" {
		t.Errorf("containers()[0] = %v, want web declaring arm64", named[0])
	}
	if named[1].GetApp() != "worker" || named[1].GetArch() != "" {
		t.Errorf("containers()[1] = %v, want worker declaring nothing, so the provider names what it runs on", named[1])
	}
}

func TestTheProjectsRuntimesAreTheFrameworksItsAppsName(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		cfg  *project.Project
		want []string
	}{
		{
			name: "a project with no apps names no framework",
			cfg:  &project.Project{},
		},
		{
			name: "an app with no framework is left out",
			cfg:  &project.Project{Apps: []project.App{{Name: "web"}}},
		},
		{
			name: "each app's framework is named",
			cfg: &project.Project{Apps: []project.App{
				{Serverless: &project.Serverless{Framework: "next"}},
				{Serverless: &project.Serverless{Framework: "node"}},
			}},
			want: []string{"next", "node"},
		},
		{
			name: "an arch does not split one runtime in two",
			cfg: &project.Project{Apps: []project.App{
				{Serverless: &project.Serverless{Framework: "next"}},
				{Serverless: &project.Serverless{Framework: "next"}, Arch: "x86_64"},
			}},
			want: []string{"next"},
		},
		{
			name: "two apps on one runtime name it once",
			cfg: &project.Project{Apps: []project.App{
				{Serverless: &project.Serverless{Framework: "next"}},
				{Serverless: &project.Serverless{Framework: "next"}},
			}},
			want: []string{"next"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := frameworks(tc.cfg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("frameworks() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAnAppThatDeclaresAContainerSendsNoFramework(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Apps: []project.App{
		{Name: "web", Compute: "container", Serverless: &project.Serverless{Framework: "next", Detected: true}},
		{Name: "api", Serverless: &project.Serverless{Framework: "node", Detected: true}},
	}}
	if got := frameworks(cfg); !reflect.DeepEqual(got, []string{"node"}) {
		t.Errorf("frameworks() = %v, want [node]: web runs its container, so no bootstrap feature its framework needs applies to it", got)
	}
}
