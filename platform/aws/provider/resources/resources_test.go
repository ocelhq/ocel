package resources

import (
	"os"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/bindings"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
)

func TestBindingFixtureExistsPerBindingType(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(componenttest.FixtureDir(t))
	if err != nil {
		t.Fatalf("read fixture directory: %v", err)
	}
	var found []string
	for _, e := range entries {
		found = append(found, e.Name())
	}

	var want []string
	for _, typ := range bindings.Types() {
		want = append(want, componenttest.FixtureFile(typ))
		if got := bindings.TypeOf(componenttest.BindingFixture(t, typ)); got != typ {
			t.Errorf("fixture %s declares a %s, want %s", componenttest.FixtureFile(typ), got, typ)
		}
	}

	slices.Sort(found)
	slices.Sort(want)
	if !slices.Equal(found, want) {
		t.Errorf("fixtures = %v, want exactly one per binding type %v", found, want)
	}
}
