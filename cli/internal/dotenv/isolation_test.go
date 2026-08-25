package dotenv_test

import (
	"os/exec"
	"path"
	"strings"
	"testing"
)

const self = "github.com/ocelhq/ocel/cli/internal/dotenv"

func TestDeployPathIsolation(t *testing.T) {
	t.Parallel()

	for _, pkg := range []string{
		"github.com/ocelhq/ocel/cli/internal/envgate",
		"github.com/ocelhq/ocel/cli/internal/varsui",
	} {
		t.Run(path.Base(pkg)+" cannot reach the dotfile", func(t *testing.T) {
			t.Parallel()
			for _, dep := range list(t, "-deps", pkg) {
				if dep == self {
					t.Errorf("%s depends on %s — the dotfile has reached the deploy path", pkg, self)
				}
			}
		})
	}

	t.Run("deploycollector cannot read the dotfile itself", func(t *testing.T) {
		t.Parallel()
		const pkg = "github.com/ocelhq/ocel/cli/internal/deploycollector"
		for _, dep := range list(t, "-f", `{{join .Imports "\n"}}`, pkg) {
			if dep == self {
				t.Errorf("%s imports %s directly — the dotfile belongs to config evaluation, never to collection", pkg, self)
			}
		}
	})
}

func list(t *testing.T, args ...string) []string {
	t.Helper()
	out, err := exec.Command("go", append([]string{"list"}, args...)...).Output()
	if err != nil {
		t.Fatalf("go list %s: %v", strings.Join(args, " "), err)
	}
	return strings.Fields(string(out))
}
