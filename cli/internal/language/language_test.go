package language_test

import (
	"os"
	"path/filepath"
	"slices"
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

func TestHasClientBundle(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		framework string
		files     []string
		want      bool
	}{
		{"a next app bundles a client", "next", nil, true},
		{"a go app bundles none", "go", []string{"package.json"}, false},
		{"an undeclared node app with a native addon bundles a client", "", []string{"package.json", "Cargo.toml"}, true},
		{"an undeclared go app with a package.json for its tooling bundles none", "", []string{"package.json", "go.mod"}, false},
		{"an app with no manifest bundles none", "", []string{"Dockerfile"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := language.HasClientBundle(tc.framework, dirContaining(t, tc.files...)); got != tc.want {
				t.Errorf("HasClientBundle = %v, want %v", got, tc.want)
			}
		})
	}
}
