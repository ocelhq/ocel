package language_test

import (
	"errors"
	"path/filepath"
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

func nodeLack() string {
	return "a package.json with no next or @sveltejs/kit dependency and no entrypoint among " + strings.Join(language.NodeEntrypoints, ", ")
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
			nodeLack(),
		},
		{
			"a package.json with only a start script, a main and an index.mjs",
			map[string]string{"package.json": `{"main":"index.mjs","scripts":{"start":"node index.mjs"}}`, "index.mjs": "\n"},
			nodeLack(),
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
			"a go module whose only main file needs cgo, beside its test",
			map[string]string{"go.mod": "module example.com/api\n", "main.go": "//go:build cgo\n\n" + mainGo, "main_test.go": "package main\n"},
			"a go.mod with no package main in its own directory",
		},
		{
			"a package.json with no app beside the crate of its native extension",
			map[string]string{"package.json": `{}`, "Cargo.toml": "[package]\nname = \"addon\"\n", "src/main.rs": "fn main() {}\n"},
			nodeLack() + " and a Cargo.toml read as a native extension, not as an app",
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
			"a pyproject.toml with no main.py and " + nodeLack(),
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

func TestDetectAppFindsNoAppInAManifestThatCannotBeRead(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"a package.json that is not JSON", map[string]string{"package.json": "{"}, "is not JSON"},
		{"a Cargo.toml that is not TOML", map[string]string{"Cargo.toml": "[package\n", "src/main.rs": "fn main() {}\n"}, "is not TOML"},
		{"a crate inheriting its edition from no workspace", map[string]string{"Cargo.toml": "[package]\nname = \"api\"\nedition.workspace = true\n\n[[bin]]\nname = \"server\"\npath = \"src/server.rs\"\n", "src/server.rs": "fn main() {}\n"}, "no directory above it holds one"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := language.DetectApp(dirWithFiles(t, tc.files))
			var missing *language.NoAppError
			if !errors.As(err, &missing) {
				t.Fatalf("DetectApp err = %v, want a NoAppError", err)
			}
			if !strings.Contains(missing.Holdings(), "cannot be read") || !strings.Contains(missing.Holdings(), tc.want) {
				t.Errorf("Holdings = %q, want it to say the manifest cannot be read: %s", missing.Holdings(), tc.want)
			}
		})
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

	if got, found, err := language.DetectFramework(dir, "cmd/api"); err != nil || !found || got != "go" {
		t.Errorf("DetectFramework = %q, %v, %v, want go: package main is where the entrypoint points", got, found, err)
	}
	if got, found, err := language.DetectFramework(dir, "src/web/index.ts"); err != nil || !found || got != "next" {
		t.Errorf("DetectFramework = %q, %v, %v, want next: the entrypoint is a TypeScript file, and the package.json names next", got, found, err)
	}
}

func TestAManifestThatCannotBeReadHoldsNoAppBesideOneThatDoes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		files map[string]string
	}{
		{"a package.json that is not JSON", map[string]string{"package.json": "{"}},
		{"a crate inheriting an edition no workspace sets", map[string]string{"Cargo.toml": "[package]\nname = \"api\"\nedition.workspace = true\n\n[[bin]]\nname = \"server\"\npath = \"src/server.rs\"\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.files["go.mod"] = "module example.com/api\n"
			tc.files["main.go"] = mainGo
			dir := dirWithFiles(t, tc.files)
			if got, ok, err := language.Detect(dir); err != nil || !ok || got != language.Go {
				t.Errorf("Detect = %q, %v, %v, want %q", got, ok, err, language.Go)
			}
			if got, err := language.DetectApp(dir); err != nil || got != "go" {
				t.Errorf("DetectApp = %q, %v, want the go app", got, err)
			}
		})
	}
}

func TestAManifestThatCannotBeReadLeavesTheLanguageUndecidedWhereNoOtherHoldsAnApp(t *testing.T) {
	t.Parallel()

	dir := dirWithFiles(t, map[string]string{"package.json": "{", "go.mod": "module example.com/api\n"})
	var ambiguity *language.AmbiguityError
	if _, _, err := language.Detect(dir); !errors.As(err, &ambiguity) || !strings.Contains(ambiguity.Holdings(), "a package.json that cannot be read") {
		t.Errorf("Detect err = %v, want an AmbiguityError saying the package.json cannot be read", err)
	}
}

func TestADeclaredEntrypointIsReadWhereTheBuildReadsIt(t *testing.T) {
	t.Parallel()

	dir := dirWithFiles(t, map[string]string{
		"go.mod":           "module example.com/api\n",
		"cmd/api/main.go":  mainGo,
		"package.json":     `{}`,
		"src/web/index.ts": "export {}\n",
	})

	cases := []struct {
		name       string
		entrypoint string
		want       string
	}{
		{"an absolute path to a TypeScript file", filepath.Join(dir, "src", "web", "index.ts"), "node"},
		{"a JavaScript file the build writes", "dist/server.js", "node"},
		{"a go file the build writes", "cmd/gen/main.go", "go"},
		{"a directory the build writes", "cmd/gen", "go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got, found, err := language.DetectFramework(dir, tc.entrypoint); err != nil || !found || got != tc.want {
				t.Errorf("DetectFramework = %q, %v, %v, want %q", got, found, err, tc.want)
			}
		})
	}
}
