package dev_test

import (
	"os/exec"
	"path"
	"strings"
	"testing"
)

const self = "github.com/ocelhq/ocel/cli/internal/dev"

func TestDeployPathIsolation(t *testing.T) {
	t.Parallel()

	for _, pkg := range []string{
		"github.com/ocelhq/ocel/cli/internal/deploy",
		"github.com/ocelhq/ocel/cli/internal/variables",
		"github.com/ocelhq/ocel/cli/internal/variableeditor",
	} {
		t.Run(path.Base(pkg)+" cannot reach the .env value reader", func(t *testing.T) {
			t.Parallel()
			for _, dep := range list(t, "-deps", pkg) {
				if dep == self {
					t.Errorf("%s depends on %s — the .env value reader has reached the deploy path", pkg, self)
				}
			}
		})
	}
}

func list(t *testing.T, args ...string) []string {
	t.Helper()
	out, err := exec.Command("go", append([]string{"list"}, args...)...).Output()
	if err != nil {
		t.Fatalf("go list %s: %v", strings.Join(args, " "), err)
	}
	return strings.Fields(string(out))
}
