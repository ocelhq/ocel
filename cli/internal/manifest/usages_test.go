package manifest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestAnAppIsAttributedInTheLanguageItsProjectResolved(t *testing.T) {
	cfg := &project.Project{
		Dir:  t.TempDir(),
		Apps: []project.App{{Name: "web", Path: "server", Framework: "python", Language: language.Python}},
	}
	apps, err := attributionApps(cfg)
	if err != nil {
		t.Fatalf("attributionApps: %v", err)
	}
	if apps[0].Language != language.Python {
		t.Errorf("Language = %q, want %q", apps[0].Language, language.Python)
	}
}

func TestANextContainerIsAttributedInAnImageOnlyWhenItIsBuiltAsOne(t *testing.T) {
	for fromFunction, inAnImage := range map[bool]bool{true: false, false: true} {
		cfg := &project.Project{
			Dir: t.TempDir(),
			Apps: []project.App{{Name: "web", Path: ".", Framework: "next", Language: language.JS,
				Compute: "container", Container: &project.Container{FromFunction: fromFunction}}},
		}
		apps, err := attributionApps(cfg)
		if err != nil {
			t.Fatalf("attributionApps: %v", err)
		}
		if apps[0].Container != inAnImage {
			t.Errorf("a Next container built from its function = %v is attributed in an image = %v, want %v", fromFunction, apps[0].Container, inAnImage)
		}
	}
}

func TestTheFixturePythonAppIsReadAsPython(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "python"))
	if err != nil {
		t.Fatalf("locate the fixture: %v", err)
	}
	cfg, err := project.Load(context.Background(), root, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	apps, err := attributionApps(cfg)
	if err != nil {
		t.Fatalf("attributionApps: %v", err)
	}
	if apps[0].Language != language.Python {
		t.Errorf("Language = %q, want %q", apps[0].Language, language.Python)
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
		Apps: []project.App{{Name: "web", Path: "server", Framework: "python", Language: language.Python}},
	}
	resources := []declaration.Resource{
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, Name: "resize", Source: "server/main.py:1", Task: &resourcesv1.TaskConfig{}},
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_WORKER, Name: "media", Source: "server/main.py:2", Worker: &resourcesv1.WorkerConfig{}},
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_CONSUMER, Name: "email", Source: "server/main.py:3", Consumer: &resourcesv1.ConsumerConfig{Topic: "orders"}},
	}

	usages, err := FindUsages(context.Background(), cfg, resources)
	if err != nil {
		t.Fatalf("FindUsages: %v", err)
	}
	if len(usages) != 1 || usages[0].Type != resourcesv1.ResourceType_RESOURCE_TYPE_TASK {
		t.Errorf("usages = %+v, want the task alone: an app triggers a task, and it never binds a worker or a consumer", usages)
	}
}

func TestAContainerAppHoldingNoLanguageManifestUsesNoResourceAndAnAppOfSeveralLanguagesIsRefused(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		project.DefaultFileName: `{"slug":"shop","apps":[{"name":"box","path":"box","compute":"container"},{"name":"web","path":"web"}]}`,
		"box/Dockerfile":        "FROM scratch\n",
		"web/requirements.txt":  "\n",
		"web/main.py":           "print(1)\n",
		"ocel/resources.py":     "print(1)\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := project.Load(context.Background(), root, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	resources := []declaration.Resource{{Type: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, Name: "resize", Source: "ocel/resources.py:1", Task: &resourcesv1.TaskConfig{}}}

	if _, err := FindUsages(context.Background(), cfg, resources); err != nil {
		t.Errorf("FindUsages = %v, want box read as using nothing: an app with no language manifest cannot import an ocel SDK", err)
	}

	cfg.Apps[0].Languages = []language.Language{language.JS, language.Python}
	if _, err := FindUsages(context.Background(), cfg, resources); err == nil || !strings.Contains(err.Error(), `set "framework"`) {
		t.Errorf("FindUsages = %v, want box refused until it sets \"framework\": its manifests name two languages", err)
	}
}
