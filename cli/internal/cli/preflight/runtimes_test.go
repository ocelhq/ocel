package preflight

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func TestProjectRuntimes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		cfg  *projectconfig.Config
		want []string
	}{
		{
			name: "a project with no apps names no runtime",
			cfg:  &projectconfig.Config{},
		},
		{
			name: "an app with no runtime is left out",
			cfg:  &projectconfig.Config{Apps: []projectconfig.App{{Name: "web"}}},
		},
		{
			name: "each app's runtime is named",
			cfg: &projectconfig.Config{Apps: []projectconfig.App{
				{Runtime: projectconfig.Runtime{Name: "next"}},
				{Runtime: projectconfig.Runtime{Name: "node"}},
			}},
			want: []string{"next", "node"},
		},
		{
			name: "an arch does not split one runtime in two",
			cfg: &projectconfig.Config{Apps: []projectconfig.App{
				{Runtime: projectconfig.Runtime{Name: "next"}},
				{Runtime: projectconfig.Runtime{Name: "next", Arch: "x86_64"}},
			}},
			want: []string{"next"},
		},
		{
			name: "two apps on one runtime name it once",
			cfg: &projectconfig.Config{Apps: []projectconfig.App{
				{Runtime: projectconfig.Runtime{Name: "next"}},
				{Runtime: projectconfig.Runtime{Name: "next"}},
			}},
			want: []string{"next"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := Runtimes(tc.cfg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Runtimes = %v, want %v", got, tc.want)
			}
		})
	}
}
