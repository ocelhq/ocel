package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func manifestDir(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(dir, manifest), []byte("\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", manifest, err)
		}
	}
	return dir
}

func TestInitWritesAConfigTheLoaderAccepts(t *testing.T) {
	manifests := map[string]struct {
		manifest string
		add      []string
	}{
		"go":     {"go.mod", []string{"go", "get", "github.com/ocelhq/ocel/sdk"}},
		"rust":   {"Cargo.toml", []string{"cargo", "add", "ocel"}},
		"python": {"pyproject.toml", []string{"uv", "add", "ocel"}},
		"node":   {"package.json", []string{"npm", "install", "ocel"}},
	}
	for language, want := range manifests {
		t.Run(language, func(t *testing.T) {
			dir := manifestDir(t, want.manifest)
			deps := newDeps()
			argv := stubPackageManager(&deps, nil)

			var stdout bytes.Buffer
			if err := runInit(context.Background(), deps, dir, "acme", initOptions{}, &stdout, &bytes.Buffer{}); err != nil {
				t.Fatalf("runInit: %v — %s", err, stdout.String())
			}

			cfg, err := projectconfig.Resolve(context.Background(), dir, "")
			if err != nil {
				t.Fatalf("the config init wrote does not load: %v", err)
			}
			if cfg.Slug != "acme" || cfg.Provider == nil || cfg.Provider.Name != "aws" {
				t.Fatalf("config = %+v", cfg)
			}
			if strings.Join(*argv, " ") != strings.Join(want.add, " ") {
				t.Fatalf("added the sdk with %v, want %v", *argv, want.add)
			}
		})
	}
}

func TestInitWritesTheSchemaThisCLIShipsWith(t *testing.T) {
	dir := manifestDir(t, "go.mod")
	deps := newDeps()
	stubPackageManager(&deps, nil)

	if err := runInit(context.Background(), deps, dir, "acme", initOptions{}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, projectconfig.DefaultFileName))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var doc struct {
		Schema string `json:"$schema"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(doc.Schema, version) || !strings.HasSuffix(doc.Schema, "ocel.schema.json") {
		t.Fatalf("$schema = %q, want it to name this CLI's version and the schema", doc.Schema)
	}
}

func TestInitWritesTypeScriptOnRequest(t *testing.T) {
	dir := manifestDir(t, "package.json")
	deps := newDeps()
	stubPackageManager(&deps, nil)

	if err := runInit(context.Background(), deps, dir, "acme", initOptions{ts: true}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, projectconfig.DefaultFileName)); err == nil {
		t.Fatal("--ts wrote a JSON config as well")
	}
	written, err := os.ReadFile(filepath.Join(dir, projectconfig.TSFileName))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(written), `from "ocel/providers/aws"`) {
		t.Fatalf("config =\n%s", written)
	}
}

func TestInitRefusesADirectoryOfSeveralLanguages(t *testing.T) {
	dir := manifestDir(t, "go.mod")
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("\n"), 0o644); err != nil {
		t.Fatalf("write Cargo.toml: %v", err)
	}
	deps := newDeps()
	stubPackageManager(&deps, nil)

	err := runInit(context.Background(), deps, dir, "acme", initOptions{}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("init picked a language from two manifests")
	}
	for _, want := range []string{"go", "rust", "--lang"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestInitTakesTheLanguageItIsGiven(t *testing.T) {
	dir := manifestDir(t, "go.mod")
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("\n"), 0o644); err != nil {
		t.Fatalf("write Cargo.toml: %v", err)
	}
	deps := newDeps()
	argv := stubPackageManager(&deps, nil)

	if err := runInit(context.Background(), deps, dir, "acme", initOptions{language: "rust"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	if strings.Join(*argv, " ") != "cargo add ocel" {
		t.Fatalf("added the sdk with %v", *argv)
	}
}

func TestInitWritesOnlyTheConfigWhenNoManifestNamesALanguage(t *testing.T) {
	dir := manifestDir(t, "")
	deps := newDeps()
	argv := stubPackageManager(&deps, nil)

	var stdout bytes.Buffer
	if err := runInit(context.Background(), deps, dir, "acme", initOptions{}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	if len(*argv) != 0 {
		t.Fatalf("ran %v in a directory holding no manifest", *argv)
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
		t.Fatal("init wrote a package.json into a directory that had none")
	}
	if _, err := projectconfig.Resolve(context.Background(), dir, ""); err != nil {
		t.Fatalf("the config init wrote does not load: %v", err)
	}
}
