package preflight

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func TestProjectFrameworks(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		cfg  *projectconfig.Config
		want []string
	}{
		{
			name: "a project with no apps names no framework",
			cfg:  &projectconfig.Config{},
		},
		{
			name: "an app with no framework is left out",
			cfg:  &projectconfig.Config{Apps: []projectconfig.App{{Name: "web"}}},
		},
		{
			name: "each app's framework is named",
			cfg:  &projectconfig.Config{Apps: []projectconfig.App{{Framework: "next"}, {Framework: "express"}}},
			want: []string{"express", "next"},
		},
		{
			name: "two apps on one framework name it once",
			cfg:  &projectconfig.Config{Apps: []projectconfig.App{{Framework: "next"}, {Framework: "next"}}},
			want: []string{"next"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := Frameworks(tc.cfg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Frameworks = %v, want %v", got, tc.want)
			}
		})
	}
}
