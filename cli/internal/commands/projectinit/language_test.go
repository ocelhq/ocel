package projectinit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/docsurl"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/configdoc"
)

func manifestDir(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(dir, manifest), []byte("{}\n"), 0o644); err != nil {
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
		"go":     {"go.mod", []string{"go", "get", "ocel.dev"}},
		"rust":   {"Cargo.toml", []string{"cargo", "add", "ocel-sdk"}},
		"python": {"pyproject.toml", []string{"uv", "add", "ocel"}},
		"node":   {"package.json", []string{"npm", "install", "ocel"}},
	}
	for language, want := range manifests {
		t.Run(language, func(t *testing.T) {
			dir := manifestDir(t, want.manifest)
			dependencies := newTestDependencies()
			argv := stubPackageManager(&dependencies, nil)
			if want.manifest == "package.json" {
				installOcelPackage(t, dir)
			}

			var stdout bytes.Buffer
			clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
			if _, err := runInit(context.Background(), dependencies, dir, "acme", initOptions{provider: "fake"}); err != nil {
				t.Fatalf("runInit: %v — %s", err, stdout.String())
			}

			cfg, err := project.Load(context.Background(), dir, "")
			if err != nil {
				t.Fatalf("the config init wrote does not load: %v", err)
			}
			if cfg.Slug != "acme" || cfg.Provider == nil || cfg.Provider.ID != "fake" {
				t.Fatalf("config = %+v", cfg)
			}
			if strings.Join(*argv, " ") != strings.Join(want.add, " ") {
				t.Fatalf("added the sdk with %v, want %v", *argv, want.add)
			}
		})
	}
}

func installOcelPackage(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found on PATH")
	}
	pkg := filepath.Join(dir, "node_modules", "ocel")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatalf("create %s: %v", pkg, err)
	}
	for name, contents := range map[string]string{
		"package.json": `{"name":"ocel","type":"module","exports":{"./config":"./config.js","./providers/fake":"./fake.js"}}`,
		"config.js":    `export const defineConfig = (config) => config;`,
		"fake.js":      `export default (options) => ({ fake: options });`,
	} {
		if err := os.WriteFile(filepath.Join(pkg, name), []byte(contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func TestInitWritesTheSchemaEveryVersionNames(t *testing.T) {
	dir := manifestDir(t, "go.mod")
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)

	if _, err := runInit(context.Background(), dependencies, dir, "acme", initOptions{provider: "fake"}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, project.DefaultFileName))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var doc struct {
		Schema string `json:"$schema"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Schema != docsurl.Schema {
		t.Fatalf("$schema = %q, want the one schema %q", doc.Schema, docsurl.Schema)
	}
}

func TestInitWritesTypeScriptForAProjectBuiltWithNode(t *testing.T) {
	dir := manifestDir(t, "package.json")
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)

	if _, err := runInit(context.Background(), dependencies, dir, "acme", initOptions{provider: "fake"}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, project.DefaultFileName)); err == nil {
		t.Fatal("init wrote a JSON config beside the TypeScript one")
	}
	written, err := os.ReadFile(filepath.Join(dir, project.TSFileName))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(written), `from "ocel/providers/fake"`) {
		t.Fatalf("config =\n%s", written)
	}
}

func TestInitWritesJSONForAProjectNodeIsNoPartOf(t *testing.T) {
	for _, manifest := range []string{"go.mod", "Cargo.toml", "pyproject.toml", ""} {
		t.Run(manifest, func(t *testing.T) {
			dir := manifestDir(t, manifest)
			dependencies := newTestDependencies()
			stubPackageManager(&dependencies, nil)

			if _, err := runInit(context.Background(), dependencies, dir, "acme", initOptions{provider: "fake"}); err != nil {
				t.Fatalf("runInit: %v", err)
			}

			if _, err := os.Stat(filepath.Join(dir, project.TSFileName)); err == nil {
				t.Fatal("init wrote a TypeScript config, which needs node and the ocel package to read")
			}
			if _, err := os.Stat(filepath.Join(dir, project.DefaultFileName)); err != nil {
				t.Fatalf("stat %s: %v", project.DefaultFileName, err)
			}
		})
	}
}

func TestInitWritesJSONOnRequestInAProjectBuiltWithNode(t *testing.T) {
	dir := manifestDir(t, "package.json")
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)

	if _, err := runInit(context.Background(), dependencies, dir, "acme", initOptions{provider: "fake", format: "json"}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, project.TSFileName)); err == nil {
		t.Fatal("--format json wrote a TypeScript config as well")
	}
	cfg, err := project.Load(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("the config init wrote does not load: %v", err)
	}
	if cfg.Path != filepath.Join(dir, project.DefaultFileName) {
		t.Fatalf("loaded %s, want %s", cfg.Path, project.DefaultFileName)
	}
}

func TestInitRefusesAFormatItDoesNotWrite(t *testing.T) {
	dir := manifestDir(t, "package.json")
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)

	_, err := runInit(context.Background(), dependencies, dir, "acme", initOptions{provider: "fake", format: "toml"})
	if err == nil || !strings.Contains(err.Error(), `"toml"`) || !strings.Contains(err.Error(), "ts, json or yaml") {
		t.Fatalf("err = %v, want the format refused, naming the ones init writes", err)
	}
	for _, name := range []string{project.TSFileName, project.DefaultFileName, project.YAMLFileName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Fatalf("wrote %s for a format init does not write", name)
		}
	}
}

func TestInitWritesYAMLOnRequest(t *testing.T) {
	dir := manifestDir(t, "go.mod")
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)

	if _, err := runInit(context.Background(), dependencies, dir, "007", initOptions{provider: "fake", format: "yaml"}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, project.DefaultFileName)); err == nil {
		t.Fatal("--format yaml wrote a JSON config as well")
	}
	cfg, err := project.Load(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("the config init wrote does not load: %v", err)
	}
	if cfg.Path != filepath.Join(dir, project.YAMLFileName) || cfg.Slug != "007" || cfg.Provider == nil || cfg.Provider.ID != "fake" {
		t.Fatalf("config = %+v", cfg)
	}
	written, err := os.ReadFile(cfg.Path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(written), "# yaml-language-server: $schema="+docsurl.Schema+"\n") {
		t.Fatalf("config names no schema:\n%s", written)
	}
}

func TestInitRefusesToWriteASecondFormOfTheConfig(t *testing.T) {
	for _, tc := range []struct {
		existing string
		opts     initOptions
		refused  string
	}{
		{project.DefaultFileName, initOptions{provider: "fake", format: "yaml"}, project.YAMLFileName},
		{project.TSFileName, initOptions{provider: "fake", format: "yaml"}, project.YAMLFileName},
		{"ocel.yml", initOptions{provider: "fake", format: "yaml"}, project.YAMLFileName},
		{project.YAMLFileName, initOptions{provider: "fake"}, project.DefaultFileName},
		{project.YAMLFileName, initOptions{provider: "fake", format: "json"}, project.DefaultFileName},
		{project.YAMLFileName, initOptions{provider: "fake", language: "node"}, project.TSFileName},
	} {
		t.Run(tc.existing+" then "+tc.refused, func(t *testing.T) {
			dir := manifestDir(t, "go.mod")
			if err := os.WriteFile(filepath.Join(dir, tc.existing), []byte("{}\n"), 0o644); err != nil {
				t.Fatalf("write %s: %v", tc.existing, err)
			}
			dependencies := newTestDependencies()
			stubPackageManager(&dependencies, nil)

			_, err := runInit(context.Background(), dependencies, dir, "acme", tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.existing) {
				t.Fatalf("error %v does not name the %s already there", err, tc.existing)
			}
			if _, err := os.Stat(filepath.Join(dir, tc.refused)); err == nil {
				t.Fatalf("wrote %s beside %s", tc.refused, tc.existing)
			}
		})
	}
}

func TestInitRefusesAFormFlagTheExplicitPathContradicts(t *testing.T) {
	for _, tc := range []struct {
		path string
		opts initOptions
		want string
	}{
		{project.DefaultFileName, initOptions{provider: "fake", format: "yaml"}, "--format yaml"},
		{"ocel.staging.yaml", initOptions{provider: "fake", format: "json"}, "--format json"},
		{"ocel.staging.json", initOptions{provider: "fake", format: "ts"}, "--format ts"},
		{"config.json", initOptions{provider: "fake"}, "config.json"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			dir := manifestDir(t, "go.mod")
			dependencies := newTestDependencies()
			stubPackageManager(&dependencies, nil)
			tc.opts.configPath = tc.path

			_, err := runInit(context.Background(), dependencies, dir, "acme", tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), tc.path) {
				t.Fatalf("error %v names neither %s nor %s", err, tc.want, tc.path)
			}
			if _, err := os.Stat(filepath.Join(dir, tc.path)); err == nil {
				t.Fatalf("wrote %s", tc.path)
			}
		})
	}
}

func TestInitWritesYAMLToAnExplicitYAMLPath(t *testing.T) {
	dir := manifestDir(t, "go.mod")
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)

	opts := initOptions{provider: "fake", format: "yaml", configPath: "ocel.staging.yml"}
	if _, err := runInit(context.Background(), dependencies, dir, "acme", opts); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	cfg, err := project.Load(context.Background(), dir, "ocel.staging.yml")
	if err != nil {
		t.Fatalf("the config init wrote does not load: %v", err)
	}
	if cfg.Slug != "acme" {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestInitRefusesADirectoryOfSeveralLanguages(t *testing.T) {
	dir := manifestDir(t, "go.mod")
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("\n"), 0o644); err != nil {
		t.Fatalf("write Cargo.toml: %v", err)
	}
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)

	_, err := runInit(context.Background(), dependencies, dir, "acme", initOptions{provider: "fake"})
	if err == nil {
		t.Fatal("init picked a language from two manifests")
	}
	for _, want := range []string{"go", "rust", "--lang"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestInitReadsACrateBesideAPackageJSONAsANodeProject(t *testing.T) {
	dir := manifestDir(t, "package.json")
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("\n"), 0o644); err != nil {
		t.Fatalf("write Cargo.toml: %v", err)
	}
	dependencies := newTestDependencies()
	argv := stubPackageManager(&dependencies, nil)

	if _, err := runInit(context.Background(), dependencies, dir, "acme", initOptions{provider: "fake"}); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	if want := "npm install ocel"; strings.Join(*argv, " ") != want {
		t.Fatalf("added the sdk with %v, want %q: the crate is the node app's native addon", *argv, want)
	}
}

func TestInitTakesTheLanguageItIsGiven(t *testing.T) {
	dir := manifestDir(t, "go.mod")
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("\n"), 0o644); err != nil {
		t.Fatalf("write Cargo.toml: %v", err)
	}
	dependencies := newTestDependencies()
	argv := stubPackageManager(&dependencies, nil)

	if _, err := runInit(context.Background(), dependencies, dir, "acme", initOptions{provider: "fake", language: "rust"}); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	if strings.Join(*argv, " ") != "cargo add ocel-sdk" {
		t.Fatalf("added the sdk with %v", *argv)
	}
}

func TestInitWritesOnlyTheConfigWhenNoManifestNamesALanguage(t *testing.T) {
	dir := manifestDir(t, "")
	dependencies := newTestDependencies()
	argv := stubPackageManager(&dependencies, nil)

	if _, err := runInit(context.Background(), dependencies, dir, "acme", initOptions{provider: "fake"}); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	if len(*argv) != 0 {
		t.Fatalf("ran %v in a directory with no manifest", *argv)
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
		t.Fatal("init wrote a package.json into a directory that had none")
	}
	if _, err := project.Load(context.Background(), dir, ""); err != nil {
		t.Fatalf("the config init wrote does not load: %v", err)
	}
}

func TestInitNamesTheProviderAloneWhereItNeedsNoOptionsAndKeysItElsewhere(t *testing.T) {
	alone, keyed := providerNamedAlone(), providerKeyed()
	for _, tc := range []struct {
		opts    initOptions
		written string
		want    string
	}{
		{initOptions{provider: alone}, project.DefaultFileName, fmt.Sprintf("  \"provider\": %q\n", alone)},
		{initOptions{provider: alone, format: "yaml"}, project.YAMLFileName, "provider: " + alone + "\n"},
		{initOptions{provider: keyed}, project.DefaultFileName, fmt.Sprintf("  \"provider\": { %q: {} }\n", keyed)},
		{initOptions{provider: keyed, format: "yaml"}, project.YAMLFileName, "provider:\n  " + keyed + ": {}\n"},
		{initOptions{provider: "fake", language: "node"}, project.TSFileName, "  provider: fakeProvider({}),\n"},
	} {
		t.Run(tc.opts.provider+" in "+tc.written, func(t *testing.T) {
			dir := manifestDir(t, "")
			dependencies := newTestDependencies()
			stubPackageManager(&dependencies, nil)

			if _, err := runInit(context.Background(), dependencies, dir, "acme", tc.opts); err != nil {
				t.Fatalf("runInit: %v", err)
			}
			written, err := os.ReadFile(filepath.Join(dir, tc.written))
			if err != nil {
				t.Fatalf("read config: %v", err)
			}
			if !strings.Contains(string(written), tc.want) {
				t.Fatalf("config =\n%s\nwant it to contain\n%s", written, tc.want)
			}
		})
	}
}

func TestInitRefusesAProviderOcelDoesNotShip(t *testing.T) {
	dir := manifestDir(t, "go.mod")
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)

	_, err := runInit(context.Background(), dependencies, dir, "acme", initOptions{provider: "unshipped"})
	if err == nil {
		t.Fatal("init wrote a config naming a provider nothing ships")
	}
	for _, want := range []string{`"unshipped"`, strings.Join(configdoc.ProviderIDs(), ", ")} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, project.DefaultFileName)); err == nil {
		t.Fatal("a config was written for a provider nothing ships")
	}
}
