package language_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/language"
)

const mainGo = "package main\n\nfunc main() {}\n"

func TestDetectAppFindsTheAppEachLanguagesBuildWouldServe(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"a next dependency", map[string]string{"package.json": `{"dependencies":{"next":"15"}}`}, "next"},
		{"a next config", map[string]string{"package.json": `{}`, "next.config.mjs": "export default {}\n"}, "next"},
		{"a sveltekit dependency", map[string]string{"package.json": `{"devDependencies":{"@sveltejs/kit":"2"}}`}, "sveltekit"},
		{"a node entrypoint under src", map[string]string{"package.json": `{}`, "src/server.ts": "export {}\n"}, "node"},
		{"a node entrypoint at the root", map[string]string{"package.json": `{}`, "app.js": "\n"}, "node"},
		{"a start script", map[string]string{"package.json": `{"scripts":{"start":"node dist/main.js"}}`, "src/main.ts": "export {}\n"}, "node"},
		{"a main the build writes", map[string]string{"package.json": `{"main":"dist/main.js","scripts":{"build":"tsc"}}`}, "node"},
		{"a main that is there", map[string]string{"package.json": `{"main":"lib/main.js"}`, "lib/main.js": "\n"}, "node"},
		{"an index.mjs", map[string]string{"package.json": `{}`, "index.mjs": "\n"}, "node"},
		{"a main.py", map[string]string{"pyproject.toml": "[project]\nname = \"api\"\n", "main.py": "\n"}, "python"},
		{"a main.py beside requirements.txt", map[string]string{"requirements.txt": "fastapi\n", "main.py": "\n"}, "python"},
		{"a main package in the directory", map[string]string{"go.mod": "module example.com/api\n", "main.go": mainGo}, "go"},
		{"a main package built without cgo", map[string]string{"go.mod": "module example.com/api\n", "main.go": "//go:build !cgo\n\n" + mainGo}, "go"},
		{"a crate building src/main.rs", map[string]string{"Cargo.toml": "[package]\nname = \"api\"\n", "src/main.rs": "fn main() {}\n"}, "rust"},
		{"a crate building one named bin", map[string]string{"Cargo.toml": "[package]\nname = \"api\"\n\n[[bin]]\nname = \"server\"\npath = \"src/server.rs\"\n", "src/server.rs": "fn main() {}\n"}, "rust"},
		{"a crate naming src/main.rs as its one bin", map[string]string{"Cargo.toml": "[package]\nname = \"api\"\n\n[[bin]]\nname = \"server\"\npath = \"src/main.rs\"\n", "src/main.rs": "fn main() {}\n"}, "rust"},
		{"an edition 2015 crate whose declared bin hides src/main.rs", map[string]string{"Cargo.toml": "[package]\nname = \"api\"\n\n[[bin]]\nname = \"server\"\npath = \"src/server.rs\"\n", "src/server.rs": "fn main() {}\n", "src/main.rs": "fn main() {}\n"}, "rust"},
		{"a workspace edition 2015 crate whose declared bin hides src/main.rs", map[string]string{"Cargo.toml": "[workspace]\n\n[workspace.package]\nedition = \"2015\"\n\n[package]\nname = \"api\"\nedition.workspace = true\n\n[[bin]]\nname = \"server\"\npath = \"src/server.rs\"\n", "src/server.rs": "fn main() {}\n", "src/main.rs": "fn main() {}\n"}, "rust"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := language.DetectApp(dirWithFiles(t, tc.files))
			if err != nil || got != tc.want {
				t.Errorf("DetectApp = %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}

const nodeLack = "a package.json with no next or @sveltejs/kit dependency, no start script, no main and no entrypoint among "

func nodeEntrypointsTried() string {
	return strings.Join(language.NodeEntrypoints, ", ") + ", index.mjs, index.cjs"
}

func TestDetectAppFindsNoAppWhereNoBuildWouldServeOne(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			"a package.json with no framework and no entrypoint",
			map[string]string{"package.json": `{"dependencies":{"ocel":"0.0.2"}}`},
			nodeLack + nodeEntrypointsTried(),
		},
		{
			"a package.json whose main names a file nothing writes",
			map[string]string{"package.json": `{"main":"index.js","scripts":{"test":"exit 1"}}`},
			nodeLack + nodeEntrypointsTried(),
		},
		{
			"a go module whose main package needs cgo",
			map[string]string{"go.mod": "module example.com/api\n", "main.go": "//go:build cgo\n\n" + mainGo},
			"a go.mod with no package main in its own directory",
		},
		{
			"an edition 2021 crate building its declared bin and src/main.rs",
			map[string]string{"Cargo.toml": "[package]\nname = \"api\"\nedition = \"2021\"\n\n[[bin]]\nname = \"server\"\npath = \"src/server.rs\"\n", "src/server.rs": "fn main() {}\n", "src/main.rs": "fn main() {}\n"},
			"a Cargo.toml building 2 binaries, where an app builds one",
		},
		{
			"a pyproject.toml with no main.py",
			map[string]string{"pyproject.toml": "[project]\nname = \"helpers\"\n", "seed.py": "\n"},
			"a pyproject.toml with no main.py",
		},
		{
			"a go module whose main package is under cmd",
			map[string]string{"go.mod": "module example.com/api\n", "cmd/api/main.go": mainGo},
			"a go.mod with no package main in its own directory",
		},
		{
			"a go module whose root package is a library",
			map[string]string{"go.mod": "module example.com/api\n", "api.go": "package api\n", "main_test.go": "package main\n"},
			"a go.mod with no package main in its own directory",
		},
		{
			"a crate building two binaries",
			map[string]string{"Cargo.toml": "[package]\nname = \"api\"\n", "src/main.rs": "fn main() {}\n", "src/bin/migrate.rs": "fn main() {}\n"},
			"a Cargo.toml building 2 binaries, where an app builds one",
		},
		{
			"a crate building no binary",
			map[string]string{"Cargo.toml": "[package]\nname = \"api\"\n", "src/lib.rs": "\n"},
			"a Cargo.toml building no binary",
		},
		{
			"no manifest",
			map[string]string{"README.md": "\n"},
			"none of go.mod, pyproject.toml, requirements.txt, package.json or Cargo.toml",
		},
		{
			"two manifests, neither holding an app",
			map[string]string{"package.json": `{}`, "pyproject.toml": "[project]\nname = \"helpers\"\n"},
			"a pyproject.toml with no main.py and " + nodeLack + nodeEntrypointsTried(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := language.DetectApp(dirWithFiles(t, tc.files))
			var missing *language.NoAppError
			if !errors.As(err, &missing) {
				t.Fatalf("DetectApp = %q, %v, want a NoAppError", got, err)
			}
			if missing.Holdings() != tc.want {
				t.Errorf("Holdings = %q, want %q", missing.Holdings(), tc.want)
			}
		})
	}
}

func TestDetectAppRefusesToChooseBetweenTwoApps(t *testing.T) {
	t.Parallel()

	dir := dirWithFiles(t, map[string]string{
		"go.mod":       "module example.com/api\n",
		"main.go":      mainGo,
		"package.json": `{}`,
		"src/index.ts": "export {}\n",
	})

	_, err := language.DetectApp(dir)
	var ambiguity *language.AmbiguityError
	if !errors.As(err, &ambiguity) {
		t.Fatalf("DetectApp err = %v, want an AmbiguityError", err)
	}
	if want := "both a go app (package main) and a node app (src/index.ts)"; ambiguity.Holdings() != want {
		t.Errorf("Holdings = %q, want %q", ambiguity.Holdings(), want)
	}
}

func TestDetectAppRefusesAPackageJSONThatIsNotJSON(t *testing.T) {
	t.Parallel()

	_, err := language.DetectApp(dirWithFiles(t, map[string]string{"package.json": "{"}))
	if err == nil || !strings.Contains(err.Error(), "is not JSON") {
		t.Fatalf("DetectApp err = %v, want the package.json refused", err)
	}
}

func TestADeclaredEntrypointDecidesWhichManifestHoldsTheApp(t *testing.T) {
	t.Parallel()

	dir := dirWithFiles(t, map[string]string{
		"go.mod":           "module example.com/api\n",
		"cmd/api/main.go":  mainGo,
		"package.json":     `{"dependencies":{"next":"15"}}`,
		"src/web/index.ts": "export {}\n",
	})

	if got, ok, err := language.Detect(dir, "cmd/api"); err != nil || !ok || got != language.Go {
		t.Errorf("Detect = %q, %v, %v, want %q: package main is where the entrypoint points", got, ok, err, language.Go)
	}
	if got, found, err := language.DetectFramework(dir, "cmd/api"); err != nil || !found || got != "go" {
		t.Errorf("DetectFramework = %q, %v, %v, want go", got, found, err)
	}
	if got, ok, err := language.Detect(dir, "src/web/index.ts"); err != nil || !ok || got != language.JS {
		t.Errorf("Detect = %q, %v, %v, want %q: the entrypoint is a TypeScript file", got, ok, err, language.JS)
	}
}
