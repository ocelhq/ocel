package providerkit_test

import (
	"os/exec"
	"strings"
	"testing"
)

var reachable = []string{
	"github.com/ocelhq/ocel/pkg/providerkit",
	"github.com/ocelhq/ocel/pkg/channel",
	"github.com/ocelhq/ocel/pkg/configdoc",
	"github.com/ocelhq/ocel/pkg/constants",
	"github.com/ocelhq/ocel/pkg/costkit",
	"github.com/ocelhq/ocel/pkg/naming",
	"github.com/ocelhq/ocel/pkg/proto",
	"github.com/ocelhq/ocel/platform/edge/contract",
}

func TestTheKitReachesOnlyTheOcelPackagesItBuildsOn(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", "./...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}

	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasPrefix(pkg, "github.com/ocelhq/ocel/") && !buildsOn(pkg) {
			t.Errorf("the kit reaches %s, which is not among the ocel packages it builds on", pkg)
		}
	}
}

func buildsOn(pkg string) bool {
	for _, root := range reachable {
		if pkg == root || strings.HasPrefix(pkg, root+"/") {
			return true
		}
	}
	return false
}
