package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestBuildRefusesAFunctionAppWhoseDirectorySaysNothingAboutWhatItIsBuiltWith(t *testing.T) {
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{
  "slug": "shop",
  "provider": { "fake": {} },
  "apps": [{ "name": "web", "path": "web" }]
}
`)
	clitest.WriteFile(t, filepath.Join(root, "web", "main.rb"), "puts 1\n")

	deps := clitest.NewDeps()
	clitest.StubBuild(&deps, nil)

	err := runBuild(context.Background(), deps, root)
	if err == nil {
		t.Fatal("runBuild = nil error, want the app refused: ocel build builds an app naming no compute as functions, and nothing says what web's are built with")
	}
	for _, want := range []string{`app "web"`, "framework"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, missing %q", err, want)
		}
	}
}
