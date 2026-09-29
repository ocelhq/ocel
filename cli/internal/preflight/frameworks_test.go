package preflight

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
)

func TestProjectRuntimes(t *testing.T) {
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

			got := Frameworks(tc.cfg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Runtimes = %v, want %v", got, tc.want)
			}
		})
	}
}
