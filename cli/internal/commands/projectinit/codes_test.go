package projectinit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/project"
)

func runFailingInit(t *testing.T, dir string, opts initOptions) (code, hint string) {
	t.Helper()
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)
	err := runInit(context.Background(), dependencies, dir, "acme", opts)
	if err == nil {
		t.Fatal("init succeeded, want a failure")
	}
	runError := clierror.NewRunError(fmt.Errorf("ocel init: %w", err))
	return runError.GetCode(), runError.GetHint()
}

func TestInitFindingAnExistingConfigReportsInitConfigExists(t *testing.T) {
	dir := manifestDir(t, "go.mod")
	if err := os.WriteFile(filepath.Join(dir, project.DefaultFileName), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, hint := runFailingInit(t, dir, initOptions{provider: "fake"})

	if code != "init.config_exists" || hint != "" {
		t.Fatalf("code, hint = %q, %q; want init.config_exists and no hint", code, hint)
	}
}

func TestInitFindingAnotherFormOfTheConfigReportsInitConfigExists(t *testing.T) {
	dir := manifestDir(t, "go.mod")
	if err := os.WriteFile(filepath.Join(dir, project.YAMLFileName), []byte("slug: acme\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, _ := runFailingInit(t, dir, initOptions{provider: "fake"})

	if code != "init.config_exists" {
		t.Fatalf("code = %q, want init.config_exists", code)
	}
}

func TestInitFindingManifestsOfSeveralLanguagesReportsInitAmbiguousLanguageHintingLang(t *testing.T) {
	dir := manifestDir(t, "go.mod")
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("\n"), 0o644); err != nil {
		t.Fatalf("write Cargo.toml: %v", err)
	}

	code, hint := runFailingInit(t, dir, initOptions{provider: "fake"})

	if code != "init.ambiguous_language" || hint != "--lang" {
		t.Fatalf("code, hint = %q, %q; want init.ambiguous_language, --lang", code, hint)
	}
}
