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
		want  language.Language
	}{
		{"a next dependency", map[string]string{"package.json": `{"dependencies":{"next":"15"}}`}, language.JS},
		{"a next config", map[string]string{"package.json": `{}`, "next.config.mjs": "export default {}\n"}, language.JS},
		{"a sveltekit dependency", map[string]string{"package.json": `{"devDependencies":{"@sveltejs/kit":"2"}}`}, language.JS},
		{"a node entrypoint under src", map[string]string{"package.json": `{}`, "src/server.ts": "export {}\n"}, language.JS},
		{"a node entrypoint at the root", map[string]string{"package.json": `{}`, "app.js": "\n"}, language.JS},
		{"a main.py", map[string]string{"pyproject.toml": "[project]\nname = \"api\"\n", "main.py": "\n"}, language.Python},
		{"a main.py beside requirements.txt", map[string]string{"requirements.txt": "fastapi\n", "main.py": "\n"}, language.Python},
		{"a main package in the directory", map[string]string{"go.mod": "module example.com/api\n", "main.go": mainGo}, language.Go},
		{"a crate building src/main.rs", map[string]string{"Cargo.toml": "[package]\nname = \"api\"\n", "src/main.rs": "fn main() {}\n"}, language.Rust},
		{"a crate building one named bin", map[string]string{"Cargo.toml": "[package]\nname = \"api\"\n\n[[bin]]\nname = \"server\"\npath = \"src/server.rs\"\n", "src/server.rs": "fn main() {}\n"}, language.Rust},
		{"a crate naming src/main.rs as its one bin", map[string]string{"Cargo.toml": "[package]\nname = \"api\"\n\n[[bin]]\nname = \"server\"\npath = \"src/main.rs\"\n", "src/main.rs": "fn main() {}\n"}, language.Rust},
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
			"a package.json with no next or @sveltejs/kit dependency and no entrypoint among " + strings.Join(language.NodeEntrypoints, ", "),
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
			"a pyproject.toml with no main.py and a package.json with no next or @sveltejs/kit dependency and no entrypoint among " + strings.Join(language.NodeEntrypoints, ", "),
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
