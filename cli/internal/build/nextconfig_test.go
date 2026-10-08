package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider"
)

func nextContainerApp(name, path string) project.App {
	return project.App{Name: name, Path: path, Compute: provider.ComputeContainer, Container: &project.Container{Framework: buildoutput.FrameworkNext}}
}

func writeNextConfig(t *testing.T, root, app, file, body string) {
	t.Helper()
	dir := filepath.Join(root, app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAServerlessNextAppThatNamesItsOwnAdapterIsRefusedWithTheSettingToDelete(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, file, body string }{
		{"top level", "next.config.mjs", `export default { adapterPath: "./my-adapter.mjs" }`},
		{"experimental", "next.config.ts", `export default { experimental: { adapterPath: "./a.mjs" } }`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			writeNextConfig(t, root, "web", tt.file, tt.body)
			cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "web")}}

			err := RefuseNextFunctionsWithOwnAdapter(cfg)
			if err == nil {
				t.Fatal("RefuseNextFunctionsWithOwnAdapter() = nil, want a refusal")
			}
			want := `app "web" sets adapterPath in ` + tt.file + `, and next build then runs that adapter in place of ocel's, which writes the output ocel deploys: delete adapterPath from ` + tt.file
			if err.Error() != want {
				t.Errorf("RefuseNextFunctionsWithOwnAdapter() = %q, want %q", err, want)
			}
		})
	}
}

func TestEveryServerlessNextAppThatNamesItsOwnAdapterIsRefused(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeNextConfig(t, root, "web", "next.config.mjs", `export default { adapterPath: "./a.mjs" }`)
	writeNextConfig(t, root, "docs", "next.config.mjs", `export default { adapterPath: "./b.mjs" }`)
	cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "web"), nextApp("docs", "docs")}}

	err := RefuseNextFunctionsWithOwnAdapter(cfg)
	if err == nil {
		t.Fatal("RefuseNextFunctionsWithOwnAdapter() = nil, want a refusal")
	}
	web, docs := strings.Index(err.Error(), `app "web"`), strings.Index(err.Error(), `app "docs"`)
	if web < 0 || docs < 0 || web > docs {
		t.Errorf("RefuseNextFunctionsWithOwnAdapter() = %q, want the web line then the docs line", err)
	}
}

func TestAServerlessNextAppWithoutItsOwnAdapterIsNotRefused(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeNextConfig(t, root, "strict", "next.config.js", "module.exports = { reactStrictMode: true }")
	writeNextConfig(t, root, "standalone", "next.config.mjs", `export default { output: "standalone" }`)
	cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("bare", "bare"), nextApp("strict", "strict"), nextApp("standalone", "standalone")}}

	if err := RefuseNextFunctionsWithOwnAdapter(cfg); err != nil {
		t.Errorf("RefuseNextFunctionsWithOwnAdapter() = %v, want nil", err)
	}
}

func TestANextContainerThatNamesItsOwnAdapterIsNotRefusedAtBuild(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeNextConfig(t, root, "web", "next.config.mjs", `export default { adapterPath: "./a.mjs" }`)
	cfg := &project.Project{Dir: root, Apps: []project.App{nextContainerApp("web", "web")}}

	if err := RefuseNextFunctionsWithOwnAdapter(cfg); err != nil {
		t.Errorf("RefuseNextFunctionsWithOwnAdapter() = %v, want nil", err)
	}
}

func TestAnAdapterPathInsideAJavaScriptCommentIsNotRefused(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, body string }{
		{"line comment", "export default {\n  // adapterPath: \"./a.js\",\n}\n"},
		{"trailing line comment", "export default {} // adapterPath: \"./a.js\"\n"},
		{"block comment", "export default {\n  /* adapterPath: \"./a.js\", */\n}\n"},
		{"multiline block comment", "export default {\n  /*\n  adapterPath: \"./a.js\",\n  */\n}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			writeNextConfig(t, root, "web", "next.config.mjs", tt.body)
			cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "web")}}

			if err := RefuseNextFunctionsWithOwnAdapter(cfg); err != nil {
				t.Errorf("RefuseNextFunctionsWithOwnAdapter() = %v, want nil", err)
			}
		})
	}
}

func TestAnAdapterPathBesideAStringHoldingCommentMarkersIsStillRefused(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeNextConfig(t, root, "web", "next.config.mjs", "export default { assetPrefix: \"https://cdn.example.com\", adapterPath: \"./a.js\", rewrites: '/* x' }")
	cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "web")}}

	if err := RefuseNextFunctionsWithOwnAdapter(cfg); err == nil {
		t.Error("RefuseNextFunctionsWithOwnAdapter() = nil, want a refusal")
	}
}

func TestAServerlessNextAppWhoseConfigOnlyNamesALookalikeOfAdapterPathIsNotRefused(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		`export default { adapterPathX: "./a.js" }`,
		`export default { adapterPathX }`,
		`export default { myadapterPath, other }`,
	} {
		root := t.TempDir()
		writeNextConfig(t, root, "web", "next.config.mjs", body)
		cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "web")}}

		if err := RefuseNextFunctionsWithOwnAdapter(cfg); err != nil {
			t.Errorf("RefuseNextFunctionsWithOwnAdapter(%q) = %v, want nil", body, err)
		}
	}
}
