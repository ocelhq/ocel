package language_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/language"
)

func dirContaining(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestManifestedNamesEveryLanguageWhoseManifestSitsInTheDirectoryInPrecedenceOrder(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		files []string
		want  []language.Language
	}{
		{"a package.json is js", []string{"package.json"}, []language.Language{language.JS}},
		{"a go.mod is go", []string{"go.mod"}, []language.Language{language.Go}},
		{"a pyproject.toml is python", []string{"pyproject.toml"}, []language.Language{language.Python}},
		{"a requirements.txt is python", []string{"requirements.txt"}, []language.Language{language.Python}},
		{"a pyproject.toml and a requirements.txt are one python project", []string{"pyproject.toml", "requirements.txt"}, []language.Language{language.Python}},
		{"a Cargo.toml is rust", []string{"Cargo.toml"}, []language.Language{language.Rust}},
		{"a crate beside a package.json is the node app's native addon", []string{"package.json", "Cargo.toml"}, []language.Language{language.JS}},
		{"a crate beside a pyproject.toml is the python app's extension module", []string{"pyproject.toml", "Cargo.toml"}, []language.Language{language.Python}},
		{"a crate beside a go.mod is a second language", []string{"go.mod", "Cargo.toml"}, []language.Language{language.Go, language.Rust}},
		{"go comes before python and js", []string{"package.json", "requirements.txt", "go.mod"}, []language.Language{language.Go, language.Python, language.JS}},
		{"a directory containing no manifest names no language", []string{"main.rb"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := language.Manifested(dirContaining(t, tc.files...)); !slices.Equal(got, tc.want) {
				t.Errorf("Manifested = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOfReadsTheLanguageAnImageOfTheDirectoryIsBuiltIn(t *testing.T) {
	t.Parallel()

	if got, ok := language.Of(dirContaining(t, "package.json", "requirements.txt", "go.mod")); !ok || got != language.Go {
		t.Errorf("Of = %q, %v, want %q: a package.json and requirements.txt beside a go.mod list what the go app's tooling reads", got, ok, language.Go)
	}
	if got, ok := language.Of(dirContaining(t, "package.json", "Cargo.toml")); !ok || got != language.JS {
		t.Errorf("Of = %q, %v, want %q", got, ok, language.JS)
	}
	if got, ok := language.Of(dirContaining(t, "Dockerfile")); ok {
		t.Errorf("Of = %q, true, want no language: nothing here is a manifest", got)
	}
}

func TestADirectoryNamedPackageJSONIsNoManifest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "package.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := language.Manifested(dir); got != nil {
		t.Errorf("Manifested = %v, want none", got)
	}
}

func TestOfAppReadsANamedFrameworkBeforeTheDirectory(t *testing.T) {
	t.Parallel()

	dir := dirContaining(t, "package.json")
	cases := []struct {
		framework string
		want      language.Language
	}{
		{"python", language.Python},
		{"next", language.JS},
		{"node", language.JS},
		{"go", language.Go},
		{"rust", language.Rust},
		{"", language.JS},
	}
	for _, tc := range cases {
		if got := language.OfApp(tc.framework, dir); got != tc.want {
			t.Errorf("OfApp(%q) = %q, want %q", tc.framework, got, tc.want)
		}
	}
}

func TestOfAppReadsAnAppWithNoManifestAsJS(t *testing.T) {
	t.Parallel()

	if got := language.OfApp("", dirContaining(t, "Dockerfile")); got != language.JS {
		t.Errorf("OfApp = %q, want %q", got, language.JS)
	}
}

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

const nextPackageJSON = `{"dependencies":{"next":"15","react":"19"}}`

func TestANextAppWinsOverAPythonProjectWithNoMainPy(t *testing.T) {
	t.Parallel()

	dir := dirWithFiles(t, map[string]string{
		"package.json":    nextPackageJSON,
		"next.config.ts":  "export default {}\n",
		"app/page.tsx":    "export default function Page() { return null }\n",
		"pyproject.toml":  "[project]\nname = \"helpers\"\n",
		"scripts/seed.py": "print('seed')\n",
	})

	if got, ok := language.Of(dir); !ok || got != language.JS {
		t.Errorf("Of = %q, %v, want %q: the pyproject.toml holds no app python serves", got, ok, language.JS)
	}
	got, ok, err := language.Detect(dir)
	if err != nil || !ok || got != language.JS {
		t.Errorf("Detect = %q, %v, %v, want %q", got, ok, err, language.JS)
	}
	framework, found, err := language.DetectFramework(dir, "")
	if err != nil || !found || framework != "next" {
		t.Errorf("DetectFramework = %q, %v, %v, want next", framework, found, err)
	}
}

func TestASvelteKitAppWinsOverARequirementsTxtWithNoMainPy(t *testing.T) {
	t.Parallel()

	dir := dirWithFiles(t, map[string]string{
		"package.json":     `{"devDependencies":{"@sveltejs/kit":"2"}}`,
		"requirements.txt": "black\n",
	})

	if got, ok, err := language.Detect(dir); err != nil || !ok || got != language.JS {
		t.Errorf("Detect = %q, %v, %v, want %q", got, ok, err, language.JS)
	}
}

func TestDetectRefusesToChooseBetweenTwoLanguagesThatEachHoldAnApp(t *testing.T) {
	t.Parallel()

	dir := dirWithFiles(t, map[string]string{
		"package.json":   nextPackageJSON,
		"pyproject.toml": "[project]\nname = \"api\"\n",
		"main.py":        "app = None\n",
	})

	_, _, err := language.Detect(dir)
	var ambiguity *language.AmbiguityError
	if !errors.As(err, &ambiguity) {
		t.Fatalf("Detect err = %v, want an AmbiguityError", err)
	}
	if !slices.Equal(ambiguity.Languages, []language.Language{language.Python, language.JS}) {
		t.Errorf("Languages = %v, want python and js", ambiguity.Languages)
	}
	if want := "both a python app (main.py) and a Next.js app"; ambiguity.Holdings() != want {
		t.Errorf("Holdings = %q, want %q", ambiguity.Holdings(), want)
	}
	if got, ok := language.Of(dir); !ok || got != language.Python {
		t.Errorf("Of = %q, %v, want %q: between two apps the precedence decides what an image is built in", got, ok, language.Python)
	}
}

func TestDetectRefusesToChooseBetweenManifestsNoneOfWhichHoldsAnApp(t *testing.T) {
	t.Parallel()

	dir := dirWithFiles(t, map[string]string{
		"package.json":   `{"dependencies":{"express":"5"}}`,
		"pyproject.toml": "[project]\nname = \"helpers\"\n",
	})

	_, _, err := language.Detect(dir)
	var ambiguity *language.AmbiguityError
	if !errors.As(err, &ambiguity) {
		t.Fatalf("Detect err = %v, want an AmbiguityError", err)
	}
	if want := "both a pyproject.toml with no main.py and a package.json with no next or @sveltejs/kit dependency and no entrypoint among " + strings.Join(language.NodeEntrypoints, ", "); ambiguity.Holdings() != want {
		t.Errorf("Holdings = %q, want %q", ambiguity.Holdings(), want)
	}
}

func TestANextAppWinsOverAGoModuleWithNoMainPackageBesideIt(t *testing.T) {
	t.Parallel()

	dir := dirWithFiles(t, map[string]string{
		"package.json":       nextPackageJSON,
		"go.mod":             "module example.com/shop\n",
		"cmd/server/main.go": "package main\n\nfunc main() {}\n",
	})

	if got, ok := language.Of(dir); !ok || got != language.JS {
		t.Errorf("Of = %q, %v, want %q: the go build compiles the directory itself, and its package is no main", got, ok, language.JS)
	}
	if got, ok, err := language.Detect(dir); err != nil || !ok || got != language.JS {
		t.Errorf("Detect = %q, %v, %v, want %q", got, ok, err, language.JS)
	}
}

func TestAGoMainPackageBesideANextPackageJSONIsAmbiguousAndReadAsGo(t *testing.T) {
	t.Parallel()

	dir := dirWithFiles(t, map[string]string{
		"package.json": nextPackageJSON,
		"go.mod":       "module example.com/shop\n",
		"main.go":      "package main\n\nfunc main() {}\n",
	})

	if got, ok := language.Of(dir); !ok || got != language.Go {
		t.Errorf("Of = %q, %v, want %q: between two apps the precedence decides what an image is built in", got, ok, language.Go)
	}
	var ambiguity *language.AmbiguityError
	if _, _, err := language.Detect(dir); !errors.As(err, &ambiguity) {
		t.Errorf("Detect err = %v, want an AmbiguityError", err)
	}
}

func TestAPackageJSONBesideAGoMainPackageWithNoEntrypointIsTheGoProjectsTooling(t *testing.T) {
	t.Parallel()

	dir := dirWithFiles(t, map[string]string{
		"package.json": `{"devDependencies":{"tailwindcss":"4"}}`,
		"go.mod":       "module example.com/shop\n",
		"main.go":      "package main\n\nfunc main() {}\n",
	})

	if got, ok, err := language.Detect(dir); err != nil || !ok || got != language.Go {
		t.Errorf("Detect = %q, %v, %v, want %q", got, ok, err, language.Go)
	}
}

func TestDetectReadsADirectoryOfOneLanguageAsThatLanguage(t *testing.T) {
	t.Parallel()

	if got, ok, err := language.Detect(dirContaining(t, "pyproject.toml")); err != nil || !ok || got != language.Python {
		t.Errorf("Detect = %q, %v, %v, want %q", got, ok, err, language.Python)
	}
	if got, ok, err := language.Detect(dirContaining(t, "Dockerfile")); err != nil || ok {
		t.Errorf("Detect = %q, %v, %v, want no language", got, ok, err)
	}
}
