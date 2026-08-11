package dotenv_test

import (
	"os/exec"
	"path"
	"strings"
	"testing"
)

func TestDeployPathIsolation(t *testing.T) {
	t.Parallel()

	const self = "github.com/ocelhq/ocel/cli/internal/dotenv"

	for _, pkg := range []string{
		"github.com/ocelhq/ocel/cli/internal/envgate",
		"github.com/ocelhq/ocel/cli/internal/deploycollector",
		"github.com/ocelhq/ocel/cli/internal/varsui",
	} {
		t.Run(path.Base(pkg)+" cannot reach the dotfile", func(t *testing.T) {
			t.Parallel()
			out, err := exec.Command("go", "list", "-deps", pkg).Output()
			if err != nil {
				t.Fatalf("go list -deps %s: %v", pkg, err)
			}
			for _, dep := range strings.Fields(string(out)) {
				if dep == self {
					t.Errorf("%s depends on %s — the dotfile has reached the deploy path", pkg, self)
				}
			}
		})
	}
}
