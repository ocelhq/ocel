package language_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/pkg/buildoutput"
)

const mainGo = "package main\n\nfunc main() {}\n"

const nextPackageJSON = `{"dependencies":{"next":"15","react":"19"}}`

func dirWithFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestResolveMatchesEachDetectorOnItsEvidence(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		files map[string]string
		want  language.Detection
	}{
		{"a next dependency", map[string]string{"package.json": nextPackageJSON}, language.Detection{Framework: "next", Language: language.JS, Evidence: "package.json depends on next"}},
		{"a next dev dependency", map[string]string{"package.json": `{"devDependencies":{"next":"15"}}`}, language.Detection{Framework: "next", Language: language.JS, Evidence: "package.json depends on next"}},
		{"a next config", map[string]string{"package.json": `{}`, "next.config.ts": "export default {}\n"}, language.Detection{Framework: "next", Language: language.JS, Evidence: "next.config.ts"}},
		{"a sveltekit dependency", map[string]string{"package.json": `{"devDependencies":{"@sveltejs/kit":"2"}}`}, language.Detection{Framework: "sveltekit", Language: language.JS, Evidence: "package.json depends on @sveltejs/kit"}},
		{"a node entrypoint under src", map[string]string{"package.json": `{}`, "src/server.ts": "export {}\n"}, language.Detection{Framework: "node", Language: language.JS, Entrypoint: "src/server.ts", Evidence: "src/server.ts"}},
		{"a node entrypoint at the root", map[string]string{"package.json": `{}`, "app.cjs": "\n"}, language.Detection{Framework: "node", Language: language.JS, Entrypoint: "app.cjs", Evidence: "app.cjs"}},
		{"a main.py beside pyproject.toml", map[string]string{"pyproject.toml": "[project]\nname = \"api\"\n", "main.py": "\n"}, language.Detection{Framework: "python", Language: language.Python, Entrypoint: "main.py", Evidence: "main.py"}},
		{"an app.py under src beside requirements.txt", map[string]string{"requirements.txt": "fastapi\n", "src/app.py": "\n"}, language.Detection{Framework: "python", Language: language.Python, Entrypoint: "src/app.py", Evidence: "src/app.py"}},
		{"an asgi.py under app beside a Pipfile", map[string]string{"Pipfile": "\n", "app/asgi.py": "\n"}, language.Detection{Framework: "python", Language: language.Python, Entrypoint: "app/asgi.py", Evidence: "app/asgi.py"}},
		{"a main package in the directory", map[string]string{"go.mod": "module example.com/api\n", "main.go": mainGo}, language.Detection{Framework: "go", Language: language.Go, Entrypoint: ".", Evidence: "package main"}},
		{"a main package built without cgo", map[string]string{"go.mod": "module example.com/api\n", "main.go": "//go:build !cgo\n\n" + mainGo}, language.Detection{Framework: "go", Language: language.Go, Entrypoint: ".", Evidence: "package main"}},
		{"a main package in cmd/api", map[string]string{"go.mod": "module example.com/api\n", "cmd/api/main.go": mainGo}, language.Detection{Framework: "go", Language: language.Go, Entrypoint: "cmd/api", Evidence: "package main in cmd/api"}},
		{"a main package in cmd/server", map[string]string{"go.mod": "module example.com/api\n", "cmd/server/main.go": mainGo}, language.Detection{Framework: "go", Language: language.Go, Entrypoint: "cmd/server", Evidence: "package main in cmd/server"}},
		{"a crate building src/main.rs", map[string]string{"Cargo.toml": "[package]\nname = \"api\"\n", "src/main.rs": "fn main() {}\n"}, language.Detection{Framework: "rust", Language: language.Rust, Evidence: "src/main.rs"}},
		{"a crate declaring a bin", map[string]string{"Cargo.toml": "[package]\nname = \"api\"\n\n[[bin]]\nname = \"server\"\npath = \"src/server.rs\"\n", "src/server.rs": "fn main() {}\n"}, language.Detection{Framework: "rust", Language: language.Rust, Evidence: "a [[bin]] target"}},
		{"a crate with a file under src/bin", map[string]string{"Cargo.toml": "[package]\nname = \"api\"\n", "src/lib.rs": "\n", "src/bin/server.rs": "fn main() {}\n"}, language.Detection{Framework: "rust", Language: language.Rust, Evidence: "src/bin/server.rs"}},
		{"a crate with a directory under src/bin", map[string]string{"Cargo.toml": "[package]\nname = \"api\"\n", "src/lib.rs": "\n", "src/bin/server/main.rs": "fn main() {}\n"}, language.Detection{Framework: "rust", Language: language.Rust, Evidence: "src/bin/server/main.rs"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := dirWithFiles(t, tc.files)
			got, err := language.Resolve(dir, dir, "")
			tc.want.Source = language.Detected
			for _, name := range language.ManifestNames() {
				if _, held := tc.files[name]; held {
					tc.want.Manifest = name
				}
			}
			if got.GoModule != nil {
				if got.GoModule.Dir != dir {
					t.Errorf("Resolve().GoModule = %+v, want the module at %s", got.GoModule, dir)
				}
				got.GoModule = nil
			}
			if err != nil || got != tc.want {
				t.Errorf("Resolve = %+v, %v, want %+v", got, err, tc.want)
			}
		})
	}
}

func TestTheNodeEntrypointsAreEachBaseWithEachExtensionInTurn(t *testing.T) {
	t.Parallel()

	want := []string{"src/server.ts", "src/server.mts", "src/server.cts", "src/server.js", "src/server.mjs", "src/server.cjs", "src/index.ts"}
	if got := language.NodeEntrypointFiles[:len(want)]; !slices.Equal(got, want) {
		t.Errorf("NodeEntrypointFiles starts %v, want %v", got, want)
	}
	if got := len(language.NodeEntrypointFiles); got != 42 {
		t.Errorf("len(NodeEntrypointFiles) = %d, want 42", got)
	}
}

func TestANestJSAppIsDetectedByItsNestjsCoreDependencyRatherThanAsANodeApp(t *testing.T) {
	t.Parallel()

	for _, manifest := range []string{`{"dependencies":{"@nestjs/core":"11"}}`, `{"devDependencies":{"@nestjs/core":"11"}}`} {
		dir := dirWithFiles(t, map[string]string{"package.json": manifest, "src/main.ts": "\n", "app.js": "\n"})
		got, err := language.Resolve(dir, dir, "")
		if err != nil || got.Framework != buildoutput.FrameworkNestJS || got.Evidence != "package.json depends on @nestjs/core" || got.Entrypoint != "" {
			t.Errorf("Resolve(%s) = %+v, %v, want nestjs on the evidence of its @nestjs/core dependency, with the entrypoint left to its build", manifest, got, err)
		}
	}
}

func TestTheFirstNodeEntrypointPresentIsTheOneDetected(t *testing.T) {
	t.Parallel()

	dir := dirWithFiles(t, map[string]string{"package.json": `{}`, "src/server.js": "\n", "src/index.ts": "\n", "app.ts": "\n"})
	got, err := language.Resolve(dir, dir, "")
	if err != nil || got.Entrypoint != "src/server.js" {
		t.Errorf("Resolve = %+v, %v, want entrypoint src/server.js", got, err)
	}
}

func TestThePythonEntrypointsAreEachNameInEachDirectoryInTurn(t *testing.T) {
	t.Parallel()

	want := []string{
		"app.py", "index.py", "server.py", "main.py", "wsgi.py", "asgi.py",
		"src/app.py", "src/index.py", "src/server.py", "src/main.py", "src/wsgi.py", "src/asgi.py",
		"app/app.py", "app/index.py", "app/server.py", "app/main.py", "app/wsgi.py", "app/asgi.py",
	}
	if !slices.Equal(language.PythonEntrypoints, want) {
		t.Errorf("PythonEntrypoints = %q, want %q", language.PythonEntrypoints, want)
	}
	for _, tc := range []struct {
		holds []string
		want  string
	}{
		{holds: []string{"main.py", "server.py"}, want: "server.py"},
		{holds: []string{"main.py", "src/app.py"}, want: "main.py"},
		{holds: []string{"app/app.py", "src/asgi.py"}, want: "src/asgi.py"},
		{holds: []string{"app/main.py", "app/__init__.py"}, want: "app/main.py"},
	} {
		files := map[string]string{"pyproject.toml": "\n"}
		for _, name := range tc.holds {
			files[name] = "\n"
		}
		dir := dirWithFiles(t, files)
		if got, err := language.Resolve(dir, dir, ""); err != nil || got.Entrypoint != tc.want {
			t.Errorf("an app holding %q is detected as %+v (err %v), want entrypoint %s, the first of PythonEntrypoints it holds", tc.holds, got, err, tc.want)
		}
	}
}

func TestAGoAppInsideAParentModuleIsDetected(t *testing.T) {
	t.Parallel()

	root := dirWithFiles(t, map[string]string{"go.mod": "module example.com/shop\n", "services/api/main.go": mainGo})
	got, err := language.Resolve(filepath.Join(root, "services", "api"), root, "")
	if err != nil || got.Framework != "go" || got.Entrypoint != "." {
		t.Errorf("Resolve = %+v, %v, want go at .", got, err)
	}
}

func TestAGoModuleAboveTheProjectRootIsNotSearched(t *testing.T) {
	t.Parallel()

	outer := dirWithFiles(t, map[string]string{"go.mod": "module example.com/shop\n", "project/api/main.go": mainGo})
	_, err := language.Resolve(filepath.Join(outer, "project", "api"), filepath.Join(outer, "project"), "")
	var undetected *language.UndetectedAppError
	if !errors.As(err, &undetected) {
		t.Fatalf("Resolve error = %v, want an UndetectedAppError", err)
	}
}

func TestANextAppWithANodeEntrypointIsNext(t *testing.T) {
	t.Parallel()

	dir := dirWithFiles(t, map[string]string{"package.json": nextPackageJSON, "src/server.ts": "\n"})
	got, err := language.Resolve(dir, dir, "")
	if err != nil || got.Framework != "next" {
		t.Errorf("Resolve = %+v, %v, want next", got, err)
	}
}

func TestResolveDetectsNothingWhereNoDetectorMatches(t *testing.T) {
	t.Parallel()

	nodeLacks := "a package.json with no next dependency or next config, no @sveltejs/kit dependency, no @nestjs/core dependency and no entrypoint among src/server, src/index, src/app, index, server, app or src/main with a .ts, .mts, .cts, .js, .mjs or .cjs extension"
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"a package.json with no framework and no entrypoint", map[string]string{"package.json": `{"dependencies":{"ocel":"0.0.2"}}`}, nodeLacks},
		{"a package.json with only a start script and a main", map[string]string{"package.json": `{"main":"dist/main.js","scripts":{"start":"node dist/main.js"}}`, "dist/main.js": "\n"}, nodeLacks},
		{"a pyproject.toml with no entrypoint", map[string]string{"pyproject.toml": "\n", "scripts/seed.py": "\n"}, "a pyproject.toml with no app.py, index.py, server.py, main.py, wsgi.py or asgi.py at its root, in src/ or in app/"},
		{"a go module whose main package needs cgo", map[string]string{"go.mod": "module example.com/api\n", "main.go": "//go:build cgo\n\n" + mainGo}, "a go.mod with no package main in ., cmd/api or cmd/server"},
		{"a go module with no main package", map[string]string{"go.mod": "module example.com/api\n", "lib.go": "package api\n"}, "a go.mod with no package main in ., cmd/api or cmd/server"},
		{"a library crate", map[string]string{"Cargo.toml": "[package]\nname = \"api\"\n\n[lib]\ncrate-type = [\"cdylib\"]\n", "src/lib.rs": "\n"}, "a Cargo.toml with no binary target"},
		{"a workspace Cargo.toml", map[string]string{"Cargo.toml": "[workspace]\nmembers = [\"api\"]\n"}, "a Cargo.toml with no binary target"},
		{"no manifest", map[string]string{"main.go": mainGo, "main.py": "\n"}, "none of package.json, pyproject.toml, requirements.txt, Pipfile, go.mod or Cargo.toml"},
		{"two manifests that each lack an app", map[string]string{"package.json": `{}`, "pyproject.toml": "\n"}, nodeLacks + " and a pyproject.toml with no app.py, index.py, server.py, main.py, wsgi.py or asgi.py at its root, in src/ or in app/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := dirWithFiles(t, tc.files)
			_, err := language.Resolve(dir, dir, "")
			var undetected *language.UndetectedAppError
			if !errors.As(err, &undetected) || undetected.Ambiguous() || err.Error() != "the project root holds "+tc.want {
				t.Errorf("Resolve error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestResolveDetectsNothingWhereSeveralDetectorsMatch(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		files      map[string]string
		frameworks []string
		want       string
	}{
		{
			"a next app beside a python app",
			map[string]string{"package.json": nextPackageJSON, "pyproject.toml": "\n", "main.py": "\n"},
			[]string{"next", "python"},
			"both a Next.js app (package.json depends on next) and a python app (main.py)",
		},
		{
			"a next app that is also a sveltekit app",
			map[string]string{"package.json": `{"dependencies":{"next":"15","@sveltejs/kit":"2"}}`},
			[]string{"next", "sveltekit"},
			"both a Next.js app (package.json depends on next) and a SvelteKit app (package.json depends on @sveltejs/kit)",
		},
		{
			"a next app that is also a nestjs app",
			map[string]string{"package.json": `{"dependencies":{"next":"15","@nestjs/core":"11"}}`},
			[]string{"next", "nestjs"},
			"both a Next.js app (package.json depends on next) and a NestJS app (package.json depends on @nestjs/core)",
		},
		{
			"a go main package beside a node entrypoint",
			map[string]string{"go.mod": "module example.com/api\n", "main.go": mainGo, "package.json": `{}`, "index.ts": "\n"},
			[]string{"node", "go"},
			"both a node app (index.ts) and a go app (package main)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := dirWithFiles(t, tc.files)
			_, err := language.Resolve(dir, dir, "")
			var undetected *language.UndetectedAppError
			if !errors.As(err, &undetected) || !undetected.Ambiguous() || err.Error() != "the project root holds "+tc.want {
				t.Fatalf("Resolve error = %v, want %q", err, tc.want)
			}
			var frameworks []string
			for _, m := range undetected.Matches {
				frameworks = append(frameworks, m.Framework)
			}
			if !slices.Equal(frameworks, tc.frameworks) {
				t.Errorf("matches = %v, want %v", frameworks, tc.frameworks)
			}
		})
	}
}

func TestAPackageJSONThatCannotBeReadMatchesNoDetector(t *testing.T) {
	t.Parallel()

	dir := dirWithFiles(t, map[string]string{"package.json": `{"dependencies":`, "next.config.mjs": "\n", "src/server.ts": "\n"})
	_, err := language.Resolve(dir, dir, "")
	var undetected *language.UndetectedAppError
	if !errors.As(err, &undetected) || !strings.HasPrefix(err.Error(), "the project root holds a package.json that cannot be read: ") || !strings.Contains(err.Error(), "is not JSON") {
		t.Errorf("Resolve error = %v, want one saying package.json cannot be read", err)
	}
}

func TestAPackageJSONThatCannotBeReadLeavesAnotherManifestsAppDetected(t *testing.T) {
	t.Parallel()

	dir := dirWithFiles(t, map[string]string{"package.json": `{`, "pyproject.toml": "\n", "main.py": "\n"})
	got, err := language.Resolve(dir, dir, "")
	if err != nil || got.Framework != "python" {
		t.Errorf("Resolve = %+v, %v, want python", got, err)
	}
}

func TestANamedFrameworkTakesTheEntrypointOnlyItsOwnDetectorFinds(t *testing.T) {
	t.Parallel()

	dir := dirWithFiles(t, map[string]string{"package.json": nextPackageJSON, "src/index.mjs": "\n", "server.py": "\n", "go.mod": "module example.com/api\n", "cmd/api/main.go": mainGo})
	cases := map[string]string{"node": "src/index.mjs", "python": "server.py", "go": "cmd/api", "next": ""}
	for framework, want := range cases {
		got, err := language.Resolve(dir, dir, framework)
		if err != nil || got.Framework != framework || got.Entrypoint != want || got.Source != language.Configured {
			t.Errorf("Resolve(%q) = %+v, %v, want %s set in config with entrypoint %q", framework, got, err, framework, want)
		}
	}
}

func TestANamedFrameworkIsWrittenInTheLanguageOfItsDetectorsManifest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cases := map[string]language.Language{"node": language.JS, "next": language.JS, "sveltekit": language.JS, "nestjs": language.JS, "go": language.Go, "python": language.Python, "rust": language.Rust}
	for _, framework := range buildoutput.Frameworks() {
		got, err := language.Resolve(dir, dir, framework)
		if err != nil || got.Language != cases[framework] || cases[framework] == "" {
			t.Errorf("Resolve(%q) = %+v, %v, want language %q", framework, got, err, cases[framework])
		}
	}
}

func TestANamedFrameworkReadsItsManifestForReal(t *testing.T) {
	t.Parallel()

	dir := dirWithFiles(t, map[string]string{"go.mod": "module example.com/api\n", "cmd/api/main.go": mainGo})
	got, err := language.Resolve(filepath.Join(dir, "cmd", "api"), dir, "go")
	if err != nil || got.GoModule == nil || got.GoModule.Dir != dir || got.GoModule.Path != "example.com/api" {
		t.Errorf("Resolve(go) = %+v, %v, want the module at %s", got, err, dir)
	}

	broken := dirWithFiles(t, map[string]string{"package.json": `{"dependencies":`, "index.ts": "\n"})
	_, err = language.Resolve(broken, broken, "node")
	if err == nil || !strings.Contains(err.Error(), "the project root holds a package.json that cannot be read") {
		t.Errorf("Resolve(node) error = %v, want the package.json refused as unreadable", err)
	}
}

func TestANamedFrameworkWhoseDetectorFindsNothingSaysWhatItLacked(t *testing.T) {
	t.Parallel()

	root := dirWithFiles(t, map[string]string{"services/api/package.json": `{}`, "services/worker/README.md": "\n"})
	got, err := language.Resolve(filepath.Join(root, "services", "api"), root, "node")
	want := "services/api holds a package.json with no entrypoint among src/server, src/index, src/app, index, server, app or src/main with a .ts, .mts, .cts, .js, .mjs or .cjs extension"
	if err != nil || got.Entrypoint != "" || got.Lacking != want {
		t.Errorf("Resolve(node) = %+v, %v, want nothing matched and %q", got, err, want)
	}

	got, err = language.Resolve(filepath.Join(root, "services", "worker"), root, "python")
	want = "services/worker holds no app.py, index.py, server.py, main.py, wsgi.py or asgi.py at its root, in src/ or in app/"
	if err != nil || got.Lacking != want {
		t.Errorf("Resolve(python) = %+v, %v, want nothing matched and %q", got, err, want)
	}
}

func TestAnUndetectedAppIsWrittenInTheOneLanguageWhoseManifestItHolds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		files map[string]string
		want  []language.Language
	}{
		{"a package.json with only a start script", map[string]string{"package.json": `{"scripts":{"start":"node dist/main.js"}}`}, []language.Language{language.JS}},
		{"a package.json that cannot be read", map[string]string{"package.json": `{`}, []language.Language{language.JS}},
		{"a pyproject.toml with no entrypoint", map[string]string{"pyproject.toml": "\n"}, []language.Language{language.Python}},
		{"a go module with no main package", map[string]string{"go.mod": "module example.com/api\n", "lib.go": "package api\n"}, []language.Language{language.Go}},
		{"a next app that is also a sveltekit app", map[string]string{"package.json": `{"dependencies":{"next":"15","@sveltejs/kit":"2"}}`}, []language.Language{language.JS}},
		{"manifests of three languages", map[string]string{"go.mod": "module example.com/api\n", "requirements.txt": "\n", "Pipfile": "\n", "package.json": `{`}, []language.Language{language.JS, language.Python, language.Go}},
		{"no manifest", map[string]string{"main.rb": "\n"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := dirWithFiles(t, tc.files)
			_, err := language.Resolve(dir, dir, "")
			var undetected *language.UndetectedAppError
			if !errors.As(err, &undetected) {
				t.Fatalf("Resolve error = %v, want an UndetectedAppError", err)
			}
			if got := undetected.Languages(); !slices.Equal(got, tc.want) {
				t.Errorf("Languages() = %v, want %v", got, tc.want)
			}
			want := language.Language("")
			if len(tc.want) == 1 {
				want = tc.want[0]
			}
			if got := undetected.Language(); got != want {
				t.Errorf("Language() = %q, want %q", got, want)
			}
		})
	}
}

func TestAnUndetectedGoAppCarriesTheModuleItSitsIn(t *testing.T) {
	t.Parallel()

	root := dirWithFiles(t, map[string]string{"go.mod": "module example.com/shop\n", "services/api/lib.go": "package api\n"})
	_, err := language.Resolve(filepath.Join(root, "services", "api"), root, "")
	var undetected *language.UndetectedAppError
	if !errors.As(err, &undetected) || undetected.GoModule() == nil || undetected.GoModule().Dir != root {
		t.Errorf("Resolve error = %v, want an UndetectedAppError carrying the module at %s", err, root)
	}
}

func TestAGoModuleAboveADirectoryHoldingNoGoSourceIsNotItsManifest(t *testing.T) {
	t.Parallel()

	root := dirWithFiles(t, map[string]string{
		"go.mod":                    "module example.com/shop\n",
		"main.go":                   mainGo,
		"web/package.json":          `{"scripts":{"start":"node server.js"}}`,
		"web/server.js":             "\n",
		"box/Dockerfile":            "FROM scratch\n",
		"both/package.json":         nextPackageJSON,
		"both/main.go":              mainGo,
		"services/api/cmd/api/a.go": mainGo,
	})

	got, err := language.Resolve(filepath.Join(root, "web"), root, "")
	if err != nil || got.Framework != buildoutput.FrameworkNode || got.GoModule != nil {
		t.Errorf("Resolve(web) = %+v, %v, want node with no go module: the go.mod at the root is the root's, and web holds no go source", got, err)
	}

	_, err = language.Resolve(filepath.Join(root, "box"), root, "")
	var undetected *language.UndetectedAppError
	if !errors.As(err, &undetected) || undetected.HasManifest() || undetected.GoModule() != nil {
		t.Errorf("Resolve(box) error = %v, want an UndetectedAppError holding no manifest and no go module", err)
	}

	_, err = language.Resolve(filepath.Join(root, "both"), root, "")
	if !errors.As(err, &undetected) || !undetected.Ambiguous() || undetected.Language() != "" || undetected.GoModule() != nil {
		t.Errorf("Resolve(both) error = %v, want next and go both matched, in no one language and so with no go module", err)
	}

	got, err = language.Resolve(filepath.Join(root, "services", "api"), root, "")
	if err != nil || got.Framework != buildoutput.FrameworkGo || got.GoModule == nil || got.GoModule.Dir != root {
		t.Errorf("Resolve(services/api) = %+v, %v, want go in the module at the root: cmd/api holds its go source", got, err)
	}
}

func TestANamedGoAppTakesTheModuleAboveItWhereverItsSourceSits(t *testing.T) {
	t.Parallel()

	root := dirWithFiles(t, map[string]string{"go.mod": "module example.com/shop\n", "services/api/tools/main.go": mainGo})
	got, err := language.Resolve(filepath.Join(root, "services", "api"), root, buildoutput.FrameworkGo)
	if err != nil || got.GoModule == nil || got.GoModule.Dir != root {
		t.Errorf("Resolve(go) = %+v, %v, want the module at %s: the app says it is go", got, err, root)
	}
}

func TestAnUndetectedAppSaysWhetherItsDirectoryHoldsAManifestAndWhetherOneCannotBeRead(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                 string
		files                map[string]string
		manifest, unreadable bool
	}{
		{"no manifest", map[string]string{"Dockerfile": "FROM scratch\n"}, false, false},
		{"a package.json with no entrypoint", map[string]string{"package.json": `{}`}, true, false},
		{"a package.json that cannot be read", map[string]string{"package.json": `{`}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := dirWithFiles(t, tc.files)
			_, err := language.Resolve(dir, dir, "")
			var undetected *language.UndetectedAppError
			if !errors.As(err, &undetected) || undetected.HasManifest() != tc.manifest || undetected.HasUnreadableManifest() != tc.unreadable {
				t.Errorf("Resolve error = %v, want HasManifest() %t and HasUnreadableManifest() %t", err, tc.manifest, tc.unreadable)
			}
		})
	}
}

func TestADirectoryNamedPackageJSONIsNoManifest(t *testing.T) {
	t.Parallel()

	dir := dirWithFiles(t, map[string]string{"package.json/index.js": "\n"})
	_, err := language.Resolve(dir, dir, "")
	var undetected *language.UndetectedAppError
	if !errors.As(err, &undetected) || len(undetected.Languages()) != 0 {
		t.Errorf("Resolve error = %v, want no manifest seen", err)
	}
}
