package clientenv

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
)

func generate(t *testing.T, dir string) error {
	t.Helper()
	return Generate([]App{{Dir: dir, Variables: []manifestbuilder.Variable{clientVar("PUBLIC_SITE_URL", "https://example.com")}}})
}

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

func TestWithMapping(t *testing.T) {
	t.Parallel()

	t.Run("resolves the entry against baseUrl", func(t *testing.T) {
		t.Parallel()

		for name, tc := range map[string]struct{ baseURL, want string }{
			"a source root":  {"./src", "../.ocel/env-client.ts"},
			"the app itself": {".", "./.ocel/env-client.ts"},
			"a nested root":  {"./app/src", "../../.ocel/env-client.ts"},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

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
	})

	t.Run("refuses to replace a base config's paths", func(t *testing.T) {
		t.Parallel()

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
	})

	t.Run("extends a config stating no paths", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		write(t, filepath.Join(dir, "tsconfig.base.json"), "{\n  \"compilerOptions\": {\n    \"strict\": true\n  }\n}\n")
		write(t, filepath.Join(dir, "tsconfig.json"), "{\n  \"extends\": \"./tsconfig.base.json\"\n}\n")

		if err := generate(t, dir); err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if got := mapped(t, filepath.Join(dir, "tsconfig.json")); len(got) != 1 || got[0] != "./.ocel/env-client.ts" {
			t.Errorf("paths[%q] = %v, want the accessor", specifier, got)
		}
	})

	t.Run("extends a config with its own paths beside", func(t *testing.T) {
		t.Parallel()

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
	})

	t.Run("inherits a base config's baseUrl", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		write(t, filepath.Join(dir, "config", "base.json"), "{\n  \"compilerOptions\": {\n    \"baseUrl\": \"../src\"\n  }\n}\n")
		write(t, filepath.Join(dir, "tsconfig.json"), "{\n  \"extends\": \"./config/base.json\"\n}\n")

		if err := generate(t, dir); err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if got := mapped(t, filepath.Join(dir, "tsconfig.json")); len(got) != 1 || got[0] != "../.ocel/env-client.ts" {
			t.Errorf("paths[%q] = %v, want the accessor stated from the inherited baseUrl", specifier, got)
		}
	})

	t.Run("refuses an unresolvable base config", func(t *testing.T) {
		t.Parallel()

		for name, extends := range map[string]string{
			"a package specifier": `"@tsconfig/next/tsconfig.json"`,
			"a missing file":      `"./nowhere.json"`,
			"a list of configs":   `["./a.json", "./b.json"]`,
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

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
	})

	t.Run("refuses a config it cannot read", func(t *testing.T) {
		t.Parallel()

		for name, source := range map[string]string{
			"a top-level array":             "[]\n",
			"an unterminated string":        "{\n  \"compilerOptions: {}\n}\n",
			"an unclosed block comment":     "{\n  /* what this project needs\n  \"compilerOptions\": {}\n}\n",
			"a paths that is not an object": "{\n  \"compilerOptions\": {\n    \"paths\": null\n  }\n}\n",
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

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
	})

	t.Run("reads a config that opens with a byte order mark", func(t *testing.T) {
		t.Parallel()

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
	})

	t.Run("honours an escaped key", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		source := "{\n  \"compilerOptions\": {\n    \"paths\": {\n      \"ocel\\u002fenv\\u002fclient\": [\"./elsewhere.ts\"]\n    }\n  }\n}\n"
		write(t, filepath.Join(dir, "tsconfig.json"), source)

		if err := generate(t, dir); err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if got := read(t, filepath.Join(dir, "tsconfig.json")); got != source {
			t.Errorf("a second mapping was added beside the one already there:\n%s", got)
		}
	})

	t.Run("leaves an app with no config alone", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()

		if err := generate(t, dir); err != nil {
			t.Fatalf("Generate: %v", err)
		}
		for _, name := range configFiles {
			if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("%s was created (err = %v)", name, err)
			}
		}
	})
}
