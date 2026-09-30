package manifest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestANamedRuntimeTellsOcelWhichLanguageAnAppIs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "server"), 0o755); err != nil {
		t.Fatalf("make the app directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "server", "main.py"), []byte("print(1)\n"), 0o644); err != nil {
		t.Fatalf("write main.py: %v", err)
	}

	cfg := &project.Project{
		Dir:  root,
		Apps: []project.App{{Name: "web", Path: "server", Serverless: &project.Serverless{Framework: "python"}}},
	}
	apps, err := attributionApps(cfg, []build.Function{{App: "web"}})
	if err != nil {
		t.Fatalf("attributionApps: %v", err)
	}
	if apps[0].Language != language.Python {
		t.Errorf("Language = %q, want %q", apps[0].Language, language.Python)
	}
}

func TestTheFixturePythonAppIsReadAsPython(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "tests", "fixtures", "sdk", "python"))
	if err != nil {
		t.Fatalf("locate the fixture: %v", err)
	}

	cfg := &project.Project{
		Dir:  root,
		Apps: []project.App{{Name: "web", Path: "server", Serverless: &project.Serverless{Framework: "python"}}},
	}
	apps, err := attributionApps(cfg, []build.Function{{App: "web"}})
	if err != nil {
		t.Fatalf("attributionApps: %v", err)
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

	cfg := &project.Project{
		Dir:  root,
		Apps: []project.App{{Name: "web", Path: "web", Compute: "container"}},
	}
	apps, err := attributionApps(cfg, nil)
	if err != nil {
		t.Fatalf("attributionApps: %v", err)
	}
	if apps[0].Language != language.JS {
		t.Errorf("Language = %q, want %q: the crate beside a package.json is the node app's native addon", apps[0].Language, language.JS)
	}
}

func TestAnAppUsesOnlyWhatItCanBindAndNeverAWorkerOrConsumer(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "server"), 0o755); err != nil {
		t.Fatalf("make the app directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "server", "main.py"), []byte("print(1)\nprint(2)\nprint(3)\n"), 0o644); err != nil {
		t.Fatalf("write main.py: %v", err)
	}
	cfg := &project.Project{
		Dir:  root,
		Apps: []project.App{{Name: "web", Path: "server", Serverless: &project.Serverless{Framework: "python"}}},
	}
	resources := []declaration.Resource{
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, Name: "resize", Source: "server/main.py:1", Task: &resourcesv1.TaskConfig{}},
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_WORKER, Name: "media", Source: "server/main.py:2", Worker: &resourcesv1.WorkerConfig{}},
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_CONSUMER, Name: "email", Source: "server/main.py:3", Consumer: &resourcesv1.ConsumerConfig{Topic: "orders"}},
	}

	usages, err := FindUsages(context.Background(), cfg, build.Output{Functions: []build.Function{{App: "web"}}}, resources)
	if err != nil {
		t.Fatalf("FindUsages: %v", err)
	}
	if len(usages) != 1 || usages[0].Type != resourcesv1.ResourceType_RESOURCE_TYPE_TASK {
		t.Errorf("usages = %+v, want the task alone: an app triggers a task, and it never binds a worker or a consumer", usages)
	}
}
