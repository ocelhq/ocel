package projectconfig

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/tailscale/hujson"
)

const committedSchemaFile = "www/public/schema/ocel.schema.json"

var interpolated = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)

var skippedDirs = map[string]bool{
	"node_modules": true,
	".ocel":        true,
	".next":        true,
	"dist":         true,
	"output":       true,
	".git":         true,
}

var jsSuffixes = []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"}

func repoDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate the test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
}

func schemaID(t *testing.T, root string) string {
	t.Helper()
	var read struct {
		ID string `json:"$id"`
	}
	data, err := os.ReadFile(filepath.Join(root, committedSchemaFile))
	if err != nil {
		t.Fatalf("read the committed schema: %v", err)
	}
	if err := json.Unmarshal(data, &read); err != nil {
		t.Fatalf("unmarshal the committed schema: %v", err)
	}
	return read.ID
}

func committedSchema(t *testing.T, root string) *jsonschema.Schema {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, committedSchemaFile))
	if err != nil {
		t.Fatalf("read the committed schema: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("parse the committed schema: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(committedSchemaFile, doc); err != nil {
		t.Fatalf("add the committed schema: %v", err)
	}
	schema, err := compiler.Compile(committedSchemaFile)
	if err != nil {
		t.Fatalf("compile the committed schema: %v", err)
	}
	return schema
}

func documentOf(t *testing.T, name string, source []byte) any {
	t.Helper()
	standard, err := hujson.Standardize(source)
	if err != nil {
		t.Fatalf("%s is not valid JSON: %v", name, err)
	}
	filled := interpolated.ReplaceAll(standard, []byte("filled-in"))
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(filled))
	if err != nil {
		t.Fatalf("%s does not parse: %v", name, err)
	}
	return value
}

func fixtureDirs(t *testing.T, root string) []string {
	t.Helper()
	var dirs []string
	fixtures := filepath.Join(root, "tests", "fixtures")
	concerns, err := os.ReadDir(fixtures)
	if err != nil {
		t.Fatalf("read the fixtures: %v", err)
	}
	for _, concern := range concerns {
		if !concern.IsDir() {
			continue
		}
		named, err := os.ReadDir(filepath.Join(fixtures, concern.Name()))
		if err != nil {
			t.Fatalf("read the %s fixtures: %v", concern.Name(), err)
		}
		for _, fixture := range named {
			if fixture.IsDir() {
				dirs = append(dirs, filepath.Join(fixtures, concern.Name(), fixture.Name()))
			}
		}
	}
	return dirs
}

func configsIn(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, _, ok := formOf(entry.Name()); ok {
			found = append(found, filepath.Join(dir, entry.Name()))
		}
	}
	return found
}

func committedConfigs(t *testing.T, root string) []string {
	t.Helper()
	found := configsIn(t, root)
	for _, dir := range fixtureDirs(t, root) {
		found = append(found, configsIn(t, dir)...)
	}
	found = append(found, configsIn(t, filepath.Join(root, "console", "web"))...)
	return found
}

func holdsJavaScript(t *testing.T, dir string) bool {
	t.Helper()
	held := false
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != dir && skippedDirs[entry.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if _, _, isConfig := formOf(entry.Name()); isConfig {
			return nil
		}
		for _, suffix := range jsSuffixes {
			if strings.HasSuffix(entry.Name(), suffix) {
				held = true
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return held
}

func TestEveryCommittedConfigValidatesAgainstTheSchema(t *testing.T) {
	root := repoDir(t)
	schema := committedSchema(t, root)
	for _, path := range committedConfigs(t, root) {
		if strings.HasSuffix(path, ".config.ts") {
			continue
		}
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if err := schema.Validate(documentOf(t, path, source)); err != nil {
			t.Errorf("%s does not validate against the committed schema: %v", path, err)
		}
	}
}

func TestEveryCommittedConfigNamesTheCommittedSchema(t *testing.T) {
	root := repoDir(t)
	want := schemaID(t, root)
	for _, path := range committedConfigs(t, root) {
		if strings.HasSuffix(path, ".config.ts") {
			continue
		}
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var read struct {
			Schema string `json:"$schema"`
		}
		standard, err := hujson.Standardize(source)
		if err != nil {
			t.Fatalf("%s is not valid JSON: %v", path, err)
		}
		if err := json.Unmarshal(standard, &read); err != nil {
			t.Fatalf("unmarshal %s: %v", path, err)
		}
		if read.Schema != want {
			t.Errorf("%s names %q, want the committed schema %q", path, read.Schema, want)
		}
	}
}

func TestExactlyOneFixtureCarriesTheTypeScriptConfig(t *testing.T) {
	root := repoDir(t)
	var carrying []string
	for _, dir := range fixtureDirs(t, root) {
		for _, path := range configsIn(t, dir) {
			if strings.HasSuffix(path, ".config.ts") {
				carrying = append(carrying, dir)
				break
			}
		}
	}
	if len(carrying) != 1 {
		t.Fatalf("the fixtures carrying a typescript config are %v, want exactly one", carrying)
	}
}

func TestAFixtureWithNoJavaScriptOfItsOwnCarriesNoNodeFiles(t *testing.T) {
	root := repoDir(t)
	for _, dir := range fixtureDirs(t, root) {
		if holdsJavaScript(t, dir) {
			continue
		}
		for _, name := range []string{"package.json", "tsconfig.json", "node_modules"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				t.Errorf("%s holds no javascript of its own and still carries %s", dir, name)
			}
		}
	}
}

func TestTheGoFixtureDeploysFromJSONAlone(t *testing.T) {
	dir := filepath.Join(repoDir(t), "tests", "fixtures", "deploy", "go")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("the go fixture is not checked out: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	for _, entry := range entries {
		if _, form, ok := formOf(entry.Name()); ok && form.suffix == ".config.ts" {
			t.Fatalf("the go fixture still carries %s", entry.Name())
		}
	}

	t.Setenv("PATH", "")
	cfg, err := Resolve(t.Context(), dir, "")
	if err != nil {
		t.Fatalf("resolve the go fixture with no node on PATH: %v", err)
	}
	if cfg.Path != filepath.Join(dir, DefaultFileName) {
		t.Fatalf("path = %q", cfg.Path)
	}
	if cfg.Provider == nil || cfg.Provider.Name != "aws" {
		t.Fatalf("provider = %+v", cfg.Provider)
	}
	if len(cfg.Apps) != 1 || cfg.Apps[0].Runtime.Name != "go" {
		t.Fatalf("apps = %+v", cfg.Apps)
	}
}
