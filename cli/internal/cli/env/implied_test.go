package env

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func setUpInlineBindingFixture(t *testing.T) string {
	t.Helper()
	root := setUpEnvFixture(t)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "`+clitest.FixtureSlug+`",
  provider: { aws: {} },
  domains: { preview: "*.preview.acme.com" },
  bindings: { postgres: { main: { url: { $env: "MAIN_DATABASE_URL" } } } },
};
`)
	return root
}

func TestRunEnvSetTakesAVariableABindingReads(t *testing.T) {
	t.Run("sets it at the project root", func(t *testing.T) {
		root := setUpInlineBindingFixture(t)
		if out := envSet(t, root, "MAIN_DATABASE_URL", "postgres://u:p@db/main", envOptions{}); !strings.Contains(out, "MAIN_DATABASE_URL") {
			t.Errorf("set stdout = %q, want it to name the key it set", out)
		}
	})

	t.Run("refuses it in a folder, since the binding reads the root value", func(t *testing.T) {
		root := setUpInlineBindingFixture(t)
		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), clitest.NewDeps(), root, "MAIN_DATABASE_URL", "postgres://u:p@db/main", envOptions{folder: "/web"}, nil, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet --folder err = nil, want a folder value for a binding's variable refused")
		}
		for _, want := range []string{"MAIN_DATABASE_URL", "bindings.postgres.main", "--folder"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want %q named", err, want)
			}
		}
	})
}
