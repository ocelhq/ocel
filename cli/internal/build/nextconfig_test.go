package build

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestAContainerNextAppBuiltStandaloneIsNamedWithTheConfigFileThatSaysSo(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeNextConfig(t, root, "web", "next.config.mjs", "export default {\n  output: \"standalone\",\n}\n")
	cfg := &project.Project{Dir: root, Apps: []project.App{nextContainerApp("web", "web")}}

	found, err := FindNextConfigConflicts(cfg, Host{ShipsNextServerRuntime: true})
	if err != nil {
		t.Fatalf("FindNextConfigConflicts() error = %v", err)
	}
	if len(found) != 1 || found[0].App != "web" || found[0].ConfigFile != "next.config.mjs" || found[0].Setting != StandaloneOutput {
		t.Errorf("FindNextConfigConflicts() = %+v, want web named with next.config.mjs as standalone output", found)
	}
}

func TestAStandaloneNextAppOnAProviderWithoutANextServerRuntimeIsNotNamed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeNextConfig(t, root, "web", "next.config.js", "module.exports = { output: 'standalone' }")
	cfg := &project.Project{Dir: root, Apps: []project.App{nextContainerApp("web", "web")}}

	found, err := FindNextConfigConflicts(cfg, Host{})
	if err != nil {
		t.Fatalf("FindNextConfigConflicts() error = %v", err)
	}
	if found != nil {
		t.Errorf("FindNextConfigConflicts() = %+v, want none", found)
	}
}

func TestANextContainerWithoutStandaloneOutputIsNotNamed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeNextConfig(t, root, "web", "next.config.ts", "export default { output: process.env.STANDALONE ? \"standalone\" : undefined }")
	writeNextConfig(t, root, "docs", "next.config.js", "module.exports = { reactStrictMode: true }")
	cfg := &project.Project{Dir: root, Apps: []project.App{nextContainerApp("web", "web"), nextContainerApp("docs", "docs"), nextContainerApp("bare", "bare")}}

	found, err := FindNextConfigConflicts(cfg, Host{ShipsNextServerRuntime: true})
	if err != nil {
		t.Fatalf("FindNextConfigConflicts() error = %v", err)
	}
	if found != nil {
		t.Errorf("FindNextConfigConflicts() = %+v, want none", found)
	}
}

func TestAServerlessNextAppBuiltStandaloneIsNotNamed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeNextConfig(t, root, "web", "next.config.mjs", `export default { output: "standalone" }`)
	cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "web")}}

	found, err := FindNextConfigConflicts(cfg, Host{ShipsNextServerRuntime: true})
	if err != nil {
		t.Fatalf("FindNextConfigConflicts() error = %v", err)
	}
	if found != nil {
		t.Errorf("FindNextConfigConflicts() = %+v, want none", found)
	}
}

func TestTheStandaloneWarningNamesTheOneLineToDelete(t *testing.T) {
	t.Parallel()

	got := NextConfigConflict{App: "web", ConfigFile: "next.config.mjs", Setting: StandaloneOutput}.Warning()
	want := `app "web" sets output: "standalone" in next.config.mjs, and a standalone server.js runs on the config its build baked in, so it never loads the cache handlers ocel adds to its image and each instance caches on its own: delete output: "standalone" from next.config.mjs and start the image with next start`
	if got != want {
		t.Errorf("Warning() = %q, want %q", got, want)
	}
}

func TestANextContainerThatNamesItsOwnAdapterIsNamedWithTheConfigFileThatSaysSo(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, file, body string }{
		{"top level", "next.config.mjs", "export default {\n  adapterPath: require.resolve(\"./my-adapter.js\"),\n}\n"},
		{"experimental", "next.config.ts", `export default { experimental: { adapterPath: "./a.js" } }`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			writeNextConfig(t, root, "web", tt.file, tt.body)
			cfg := &project.Project{Dir: root, Apps: []project.App{nextContainerApp("web", "web")}}

			found, err := FindNextConfigConflicts(cfg, Host{ShipsNextServerRuntime: true})
			if err != nil {
				t.Fatalf("FindNextConfigConflicts() error = %v", err)
			}
			want := []NextConfigConflict{{App: "web", ConfigFile: tt.file, Setting: OwnAdapter}}
			if !reflect.DeepEqual(found, want) {
				t.Errorf("FindNextConfigConflicts() = %+v, want %+v", found, want)
			}
		})
	}
}

func TestANextContainerThatBuildsStandaloneAndNamesItsOwnAdapterIsNamedForBoth(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeNextConfig(t, root, "web", "next.config.mjs", "export default {\n  adapterPath: \"./a.js\",\n  output: \"standalone\",\n}\n")
	cfg := &project.Project{Dir: root, Apps: []project.App{nextContainerApp("web", "web")}}

	found, err := FindNextConfigConflicts(cfg, Host{ShipsNextServerRuntime: true})
	if err != nil {
		t.Fatalf("FindNextConfigConflicts() error = %v", err)
	}
	want := []NextConfigConflict{
		{App: "web", ConfigFile: "next.config.mjs", Setting: StandaloneOutput},
		{App: "web", ConfigFile: "next.config.mjs", Setting: OwnAdapter},
	}
	if !reflect.DeepEqual(found, want) {
		t.Errorf("FindNextConfigConflicts() = %+v, want %+v", found, want)
	}
}

func TestAServerlessNextAppThatNamesItsOwnAdapterIsNotNamed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeNextConfig(t, root, "web", "next.config.mjs", `export default { adapterPath: "./a.js" }`)
	cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "web")}}

	found, err := FindNextConfigConflicts(cfg, Host{ShipsNextServerRuntime: true})
	if err != nil {
		t.Fatalf("FindNextConfigConflicts() error = %v", err)
	}
	if found != nil {
		t.Errorf("FindNextConfigConflicts() = %+v, want none", found)
	}
}

func TestANextContainerThatNamesItsOwnAdapterOnAProviderWithoutANextServerRuntimeIsNotNamed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeNextConfig(t, root, "web", "next.config.mjs", `export default { adapterPath: "./a.js" }`)
	cfg := &project.Project{Dir: root, Apps: []project.App{nextContainerApp("web", "web")}}

	found, err := FindNextConfigConflicts(cfg, Host{})
	if err != nil {
		t.Fatalf("FindNextConfigConflicts() error = %v", err)
	}
	if found != nil {
		t.Errorf("FindNextConfigConflicts() = %+v, want none", found)
	}
}

func TestTheOwnAdapterWarningNamesTheSettingToDelete(t *testing.T) {
	t.Parallel()

	got := NextConfigConflict{App: "web", ConfigFile: "next.config.mjs", Setting: OwnAdapter}.Warning()
	want := `app "web" sets adapterPath in next.config.mjs, and next start loads that adapter in place of the one ocel adds to its image, so it never loads the cache handlers ocel ships and each instance caches on its own: delete adapterPath from next.config.mjs`
	if got != want {
		t.Errorf("Warning() = %q, want %q", got, want)
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

func TestANextConfigSettingInsideAComment(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, body string }{
		{"line comment adapter", "export default {\n  // adapterPath: \"./a.js\",\n}\n"},
		{"trailing line comment adapter", "export default {} // adapterPath: \"./a.js\"\n"},
		{"block comment adapter", "export default {\n  /* adapterPath: \"./a.js\", */\n}\n"},
		{"multiline block comment adapter", "export default {\n  /*\n  adapterPath: \"./a.js\",\n  */\n}\n"},
		{"line comment standalone", "export default {\n  // output: \"standalone\",\n}\n"},
		{"block comment standalone", "export default { /* output: 'standalone' */ }"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			writeNextConfig(t, root, "web", "next.config.mjs", tt.body)
			cfg := &project.Project{Dir: root, Apps: []project.App{nextContainerApp("web", "web"), nextApp("fn", "web")}}

			found, err := FindNextConfigConflicts(cfg, Host{ShipsNextServerRuntime: true})
			if err != nil {
				t.Fatalf("FindNextConfigConflicts() error = %v", err)
			}
			if found != nil {
				t.Errorf("FindNextConfigConflicts() = %+v, want none", found)
			}
			if err := RefuseNextFunctionsWithOwnAdapter(cfg); err != nil {
				t.Errorf("RefuseNextFunctionsWithOwnAdapter() = %v, want nil", err)
			}
		})
	}
}

func TestANextConfigSettingBesideAStringHoldingCommentMarkersIsStillNamed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeNextConfig(t, root, "web", "next.config.mjs", "export default { assetPrefix: \"https://cdn.example.com\", adapterPath: \"./a.js\", rewrites: '/* x', output: 'standalone' }")
	cfg := &project.Project{Dir: root, Apps: []project.App{nextContainerApp("web", "web")}}

	found, err := FindNextConfigConflicts(cfg, Host{ShipsNextServerRuntime: true})
	if err != nil {
		t.Fatalf("FindNextConfigConflicts() error = %v", err)
	}
	want := []NextConfigConflict{
		{App: "web", ConfigFile: "next.config.mjs", Setting: StandaloneOutput},
		{App: "web", ConfigFile: "next.config.mjs", Setting: OwnAdapter},
	}
	if !reflect.DeepEqual(found, want) {
		t.Errorf("FindNextConfigConflicts() = %+v, want %+v", found, want)
	}
}
