package dotenv_test

import (
	"os/exec"
	"strings"
	"testing"
)

// The dotfile is dev's store and only dev's. A preview or production deploy
// resolves from the shared variable store through the provider, and a file on
// one developer's machine must not be able to reach that path at all — so this
// is pinned by the import graph rather than by discipline: the packages the
// deploy path is made of cannot reach this one, whatever anyone later writes in
// them.
func TestTheDeployPathCannotReachTheDotfile(t *testing.T) {
	const self = "github.com/ocelhq/ocel/cli/internal/dotenv"

	for _, pkg := range []string{
		"github.com/ocelhq/ocel/cli/internal/envgate",
		"github.com/ocelhq/ocel/cli/internal/deploycollector",
		"github.com/ocelhq/ocel/cli/internal/varsui",
	} {
		out, err := exec.Command("go", "list", "-deps", pkg).Output()
		if err != nil {
			t.Fatalf("go list -deps %s: %v", pkg, err)
		}
		for _, dep := range strings.Fields(string(out)) {
			if dep == self {
				t.Errorf("%s depends on %s — the dotfile has reached the deploy path", pkg, self)
			}
		}
	}
}
