package deploy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func TestADetectedAppIsReadInTheLanguageOfTheProjectItSitsIn(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/web\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	cfg := &projectconfig.Config{Dir: root}
	apps, err := toAttributionApps(cfg, []manifestbuilder.Function{{App: "web"}}, "", "ocel.config.ts")
	if err != nil {
		t.Fatalf("toAttributionApps: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("toAttributionApps returned %d apps, want 1", len(apps))
	}
	if apps[0].Language != language.Go {
		t.Errorf("Language = %q, want %q", apps[0].Language, language.Go)
	}
}

func TestANamedRuntimeTellsOcelWhichLanguageAnAppIs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "server"), 0o755); err != nil {
		t.Fatalf("make the app directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "server", "main.py"), []byte("print(1)\n"), 0o644); err != nil {
		t.Fatalf("write main.py: %v", err)
	}

	cfg := &projectconfig.Config{
		Dir:  root,
		Apps: []projectconfig.App{{Name: "web", Path: "server", Framework: projectconfig.Framework{Name: "python"}}},
	}
	apps, err := toAttributionApps(cfg, []manifestbuilder.Function{{App: "web"}}, "", "ocel.config.ts")
	if err != nil {
		t.Fatalf("toAttributionApps: %v", err)
	}
	if apps[0].Language != language.Python {
		t.Errorf("Language = %q, want %q", apps[0].Language, language.Python)
	}
}

func TestTheFixturePythonAppIsReadAsPython(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "tests", "fixtures", "sdk", "python"))
	if err != nil {
		t.Fatalf("locate the fixture: %v", err)
	}

	cfg := &projectconfig.Config{
		Dir:  root,
		Apps: []projectconfig.App{{Name: "web", Path: "server", Framework: projectconfig.Framework{Name: "python"}}},
	}
	apps, err := toAttributionApps(cfg, []manifestbuilder.Function{{App: "web"}}, "", "ocel.config.ts")
	if err != nil {
		t.Fatalf("toAttributionApps: %v", err)
	}
	if apps[0].Language != language.Python {
		t.Errorf("Language = %q, want %q", apps[0].Language, language.Python)
	}
}

func TestAContainerAppWithACrateBesideItsPackageJSONIsReadAsJS(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "web")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatalf("make the app directory: %v", err)
	}
	for name, body := range map[string]string{"package.json": `{"name":"web"}`, "Cargo.toml": "[package]\nname = \"web-addon\"\n"} {
		if err := os.WriteFile(filepath.Join(app, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	cfg := &projectconfig.Config{
		Dir:  root,
		Apps: []projectconfig.App{{Name: "web", Path: "web", Compute: "container"}},
	}
	apps, err := toAttributionApps(cfg, nil, "container", "ocel.config.ts")
	if err != nil {
		t.Fatalf("toAttributionApps: %v", err)
	}
	if apps[0].Language != language.JS {
		t.Errorf("Language = %q, want %q: the crate beside a package.json is the node app's native addon", apps[0].Language, language.JS)
	}
}
