package language_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/language"
)

func dirWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestDeclaredFrameworkVersionIsTheMajorOfTheRangeThePackageManifestDeclares(t *testing.T) {
	t.Parallel()

	cases := []struct {
		framework string
		manifest  string
		want      string
	}{
		{"next", `{"dependencies":{"next":"^15.1.0"}}`, "15"},
		{"next", `{"devDependencies":{"next":"~14.2.3"}}`, "14"},
		{"next", `{"dependencies":{"next":">=13 <16"}}`, "13"},
		{"sveltekit", `{"devDependencies":{"@sveltejs/kit":"2.5.0"}}`, "2"},
		{"nestjs", `{"dependencies":{"@nestjs/core":"^11.0.1"}}`, "11"},
		{"next", `{"dependencies":{"next":"latest"}}`, ""},
		{"next", `{"dependencies":{"next":"workspace:*"}}`, ""},
		{"next", `{"dependencies":{"next":"workspace:^15.0.0"}}`, ""},
		{"next", `{"dependencies":{"next":"file:../next-15"}}`, ""},
		{"next", `{"dependencies":{"next":"github:vercel/next.js#v15.0.0"}}`, ""},
		{"next", `{"dependencies":{"next":"https://example.com/next-15.tgz"}}`, ""},
		{"next", `{"dependencies":{"next":"npm:next@15.2.0"}}`, "15"},
		{"next", `{"dependencies":{"next":"v15.0.0"}}`, "15"},
		{"next", `{"dependencies":{"react":"19.0.0"}}`, ""},
		{"next", `not json`, ""},
		{"node", `{"dependencies":{"node":"22.0.0"}}`, ""},
		{"go", `{"dependencies":{"next":"15.0.0"}}`, ""},
	}
	for _, tc := range cases {
		dir := dirWith(t, map[string]string{"package.json": tc.manifest})
		if got := language.DeclaredFrameworkVersion(tc.framework, dir); got != tc.want {
			t.Errorf("DeclaredFrameworkVersion(%q) with %s = %q, want %q", tc.framework, tc.manifest, got, tc.want)
		}
	}
}

func TestDeclaredFrameworkVersionWithNoPackageManifestIsEmpty(t *testing.T) {
	t.Parallel()

	if got := language.DeclaredFrameworkVersion("next", t.TempDir()); got != "" {
		t.Errorf("DeclaredFrameworkVersion = %q, want empty", got)
	}
}

func TestDeclaredRuntimeNamesTheRuntimeOfEachLanguageAndTheVersionItsFilesDeclare(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		language language.Language
		files    map[string]string
		runtime  string
		version  string
	}{
		{"node engines", language.JS, map[string]string{"package.json": `{"engines":{"node":">=20.11"}}`}, "node", "20"},
		{"nvmrc", language.JS, map[string]string{".nvmrc": "v22.3.0\n"}, "node", "22"},
		{"node-version", language.JS, map[string]string{".node-version": "18\n"}, "node", "18"},
		{"engines before nvmrc", language.JS, map[string]string{"package.json": `{"engines":{"node":"^22"}}`, ".nvmrc": "20"}, "node", "22"},
		{"lts alias", language.JS, map[string]string{".nvmrc": "lts/*\n"}, "node", ""},
		{"go directive", language.Go, map[string]string{"go.mod": "module example.com/app\n\ngo 1.23.4\n\ntoolchain go1.24.0\n"}, "go", "1.23"},
		{"python-version", language.Python, map[string]string{".python-version": "3.12.4\n"}, "python", "3.12"},
		{"requires-python", language.Python, map[string]string{"pyproject.toml": "[project]\nname = \"app\"\nrequires-python = \">=3.11\"\n"}, "python", "3.11"},
		{"rust-toolchain.toml", language.Rust, map[string]string{"rust-toolchain.toml": "[toolchain]\nchannel = \"1.82.0\"\n"}, "rust", "1.82"},
		{"rust-toolchain", language.Rust, map[string]string{"rust-toolchain": "1.80\n"}, "rust", "1.80"},
		{"rust stable", language.Rust, map[string]string{"rust-toolchain": "stable\n"}, "rust", ""},
		{"rust dated nightly", language.Rust, map[string]string{"rust-toolchain.toml": "[toolchain]\nchannel = \"nightly-2024.01.01\"\n"}, "rust", ""},
		{"nvmrc path", language.JS, map[string]string{".nvmrc": "../node-22\n"}, "node", ""},
		{"nothing declared", language.Go, nil, "go", ""},
	}
	for _, tc := range cases {
		runtime, version := language.DeclaredRuntime(tc.language, dirWith(t, tc.files))
		if runtime != tc.runtime || version != tc.version {
			t.Errorf("%s: DeclaredRuntime = (%q, %q), want (%q, %q)", tc.name, runtime, version, tc.runtime, tc.version)
		}
	}
}

func TestDeclaredRuntimeReadsTheFirstDirectoryThatDeclaresAVersion(t *testing.T) {
	t.Parallel()

	app := dirWith(t, map[string]string{"package.json": `{"name":"web"}`})
	root := dirWith(t, map[string]string{".nvmrc": "22"})

	if _, version := language.DeclaredRuntime(language.JS, app, root); version != "22" {
		t.Errorf("version = %q, want the root's 22", version)
	}
}
