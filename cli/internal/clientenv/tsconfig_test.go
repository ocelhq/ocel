package clientenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
)

// generate runs the accessor generation for one app directory.
func generate(t *testing.T, dir string) error {
	t.Helper()
	return Generate([]App{{Dir: dir, Variables: []manifestbuilder.Variable{clientVar("PUBLIC_SITE_URL", "https://example.com")}}})
}

// mapped is the paths value the config at path resolves the specifier to.
func mapped(t *testing.T, path string) []string {
	t.Helper()
	var parsed struct {
		CompilerOptions struct {
			Paths map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(read(t, path), "\ufeff")), &parsed); err != nil {
		t.Fatalf("config is no longer parseable (%v):\n%s", err, read(t, path))
	}
	return parsed.CompilerOptions.Paths[specifier]
}

// TestGenerate_ResolvesTheEntryAgainstBaseUrl proves the mapping points at the
// accessor and not at wherever the app's baseUrl happens to make that spelling
// land. TypeScript resolves a paths value against baseUrl, so a fixed
// "./.ocel/..." in an app with a baseUrl of "./src" names a file that does not
// exist — and says nothing about it until a browser reads undefined.
func TestGenerate_ResolvesTheEntryAgainstBaseUrl(t *testing.T) {
	for name, tc := range map[string]struct{ baseURL, want string }{
		"a source root":  {"./src", "../.ocel/env-client.ts"},
		"the app itself": {".", "./.ocel/env-client.ts"},
		"a nested root":  {"./app/src", "../../.ocel/env-client.ts"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "tsconfig.json"), "{\n  \"compilerOptions\": {\n    \"baseUrl\": \""+tc.baseURL+"\"\n  }\n}\n")

			if err := generate(t, dir); err != nil {
				t.Fatalf("Generate: %v", err)
			}

			got := mapped(t, filepath.Join(dir, "tsconfig.json"))
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("paths[%q] = %v, want [%q]", specifier, got, tc.want)
			}
		})
	}
}

// TestGenerate_RefusesToReplaceABaseConfigsPaths proves the one edit that
// would break a project outright is not made. TypeScript does not merge paths
// across extends: a paths object written into a config that inherits one
// replaces it whole, so every alias the base declared stops resolving. The
// config is left as the developer wrote it and the refusal states the entry to
// add.
func TestGenerate_RefusesToReplaceABaseConfigsPaths(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "tsconfig.base.json"), "{\n  \"compilerOptions\": {\n    \"paths\": {\n      \"@/*\": [\"./src/*\"]\n    }\n  }\n}\n")
	source := "{\n  \"extends\": \"./tsconfig.base.json\",\n  \"compilerOptions\": {\n    \"strict\": true\n  }\n}\n"
	write(t, filepath.Join(dir, "tsconfig.json"), source)

	err := generate(t, dir)
	if err == nil {
		t.Fatal("Generate = nil for a config whose paths are inherited, want a refusal")
	}
	for _, want := range []string{"tsconfig.json", "extends", specifier, "./.ocel/env-client.ts"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
	if got := read(t, filepath.Join(dir, "tsconfig.json")); got != source {
		t.Errorf("the config was edited anyway:\n%s", got)
	}
}

// TestGenerate_ExtendsAConfigStatingNoPaths proves the refusal is about the
// paths that would be lost and nothing else: a base config with none loses
// nothing, so the entry is written as it would be anywhere.
func TestGenerate_ExtendsAConfigStatingNoPaths(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "tsconfig.base.json"), "{\n  \"compilerOptions\": {\n    \"strict\": true\n  }\n}\n")
	write(t, filepath.Join(dir, "tsconfig.json"), "{\n  \"extends\": \"./tsconfig.base.json\"\n}\n")

	if err := generate(t, dir); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := mapped(t, filepath.Join(dir, "tsconfig.json")); len(got) != 1 || got[0] != "./.ocel/env-client.ts" {
		t.Errorf("paths[%q] = %v, want the accessor", specifier, got)
	}
}

// TestGenerate_ExtendsAConfigWithItsOwnPathsBeside proves an app that already
// states paths of its own has already replaced the base's, so adding an entry
// to that object takes nothing away.
func TestGenerate_ExtendsAConfigWithItsOwnPathsBeside(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "tsconfig.base.json"), "{\n  \"compilerOptions\": {\n    \"paths\": {\n      \"@/*\": [\"./src/*\"]\n    }\n  }\n}\n")
	write(t, filepath.Join(dir, "tsconfig.json"), "{\n  \"extends\": \"./tsconfig.base.json\",\n  \"compilerOptions\": {\n    \"paths\": {\n      \"~/*\": [\"./app/*\"]\n    }\n  }\n}\n")

	if err := generate(t, dir); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	updated := read(t, filepath.Join(dir, "tsconfig.json"))
	if !strings.Contains(updated, `"~/*": ["./app/*"]`) {
		t.Errorf("the app's own alias was lost:\n%s", updated)
	}
	if got := mapped(t, filepath.Join(dir, "tsconfig.json")); len(got) != 1 || got[0] != "./.ocel/env-client.ts" {
		t.Errorf("paths[%q] = %v, want the accessor", specifier, got)
	}
}

// TestGenerate_InheritsABaseConfigsBaseUrl proves a baseUrl is honoured
// wherever it is stated. A base config's own relative paths are resolved
// against the directory that config sits in, so the entry has to be stated
// from there and not from the child's directory.
func TestGenerate_InheritsABaseConfigsBaseUrl(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config", "base.json"), "{\n  \"compilerOptions\": {\n    \"baseUrl\": \"../src\"\n  }\n}\n")
	write(t, filepath.Join(dir, "tsconfig.json"), "{\n  \"extends\": \"./config/base.json\"\n}\n")

	if err := generate(t, dir); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := mapped(t, filepath.Join(dir, "tsconfig.json")); len(got) != 1 || got[0] != "../.ocel/env-client.ts" {
		t.Errorf("paths[%q] = %v, want the accessor stated from the inherited baseUrl", specifier, got)
	}
}

// TestGenerate_RefusesAnUnresolvableBaseConfig proves the CLI does not guess:
// a base config it cannot read could state paths, and writing an entry without
// knowing is the destructive case again.
func TestGenerate_RefusesAnUnresolvableBaseConfig(t *testing.T) {
	for name, extends := range map[string]string{
		"a package specifier": `"@tsconfig/next/tsconfig.json"`,
		"a missing file":      `"./nowhere.json"`,
		"a list of configs":   `["./a.json", "./b.json"]`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			source := "{\n  \"extends\": " + extends + "\n}\n"
			write(t, filepath.Join(dir, "tsconfig.json"), source)

			err := generate(t, dir)
			if err == nil {
				t.Fatal("Generate = nil for a base config ocel cannot read, want a refusal")
			}
			if !strings.Contains(err.Error(), specifier) {
				t.Errorf("error = %q, want it to state the mapping to add", err)
			}
			if got := read(t, filepath.Join(dir, "tsconfig.json")); got != source {
				t.Errorf("the config was edited anyway:\n%s", got)
			}
		})
	}
}

// TestGenerate_RefusesAConfigItCannotRead proves the failure is reported
// rather than swallowed. Each of these once produced a silent no-op: the
// accessor written, the mapping absent, and the first evidence a thrown read
// in a browser.
func TestGenerate_RefusesAConfigItCannotRead(t *testing.T) {
	for name, source := range map[string]string{
		"a top-level array":             "[]\n",
		"an unterminated string":        "{\n  \"compilerOptions: {}\n}\n",
		"an unclosed block comment":     "{\n  /* what this project needs\n  \"compilerOptions\": {}\n}\n",
		"a paths that is not an object": "{\n  \"compilerOptions\": {\n    \"paths\": null\n  }\n}\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "tsconfig.json"), source)

			err := generate(t, dir)
			if err == nil {
				t.Fatal("Generate = nil for a config ocel cannot read, want a refusal")
			}
			for _, want := range []string{"tsconfig.json", specifier, "./.ocel/env-client.ts"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to name %q", err, want)
				}
			}
			if got := read(t, filepath.Join(dir, "tsconfig.json")); got != source {
				t.Errorf("the config was edited anyway:\n%s", got)
			}
		})
	}
}

// TestGenerate_ReadsAConfigThatOpensWithAByteOrderMark proves an editor's
// invisible three bytes are not the difference between a working mapping and a
// silent one.
func TestGenerate_ReadsAConfigThatOpensWithAByteOrderMark(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "tsconfig.json"), "\ufeff{\n  \"compilerOptions\": {}\n}\n")

	if err := generate(t, dir); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.HasPrefix(read(t, filepath.Join(dir, "tsconfig.json")), "\ufeff") {
		t.Error("the byte order mark was dropped")
	}
	if got := mapped(t, filepath.Join(dir, "tsconfig.json")); len(got) != 1 || got[0] != "./.ocel/env-client.ts" {
		t.Errorf("paths[%q] = %v, want the accessor", specifier, got)
	}
}

// TestGenerate_HonoursAnEscapedKey proves a key is read as JSON says it reads.
// A developer who wrote the mapping with an escape wrote the mapping, and a
// second one beside it is a config with the same key twice.
func TestGenerate_HonoursAnEscapedKey(t *testing.T) {
	dir := t.TempDir()
	source := "{\n  \"compilerOptions\": {\n    \"paths\": {\n      \"ocel\\u002fenv\\u002fclient\": [\"./elsewhere.ts\"]\n    }\n  }\n}\n"
	write(t, filepath.Join(dir, "tsconfig.json"), source)

	if err := generate(t, dir); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := read(t, filepath.Join(dir, "tsconfig.json")); got != source {
		t.Errorf("a second mapping was added beside the one already there:\n%s", got)
	}
}

// TestGenerate_LeavesAnAppWithNoConfigAlone proves the mapping is written into
// a config the app has and never into one ocel invents.
func TestGenerate_LeavesAnAppWithNoConfigAlone(t *testing.T) {
	dir := t.TempDir()

	if err := generate(t, dir); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, name := range configFiles {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s was created (err = %v)", name, err)
		}
	}
}
