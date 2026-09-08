package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func pythonFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "infra", "__init__.py"), "")
	return root
}

func TestThePythonLauncherRunsAScriptThatImportsTheInfraPackage(t *testing.T) {
	configDir := pythonFixture(t)
	root := Root{Dir: filepath.Join(configDir, "infra"), Language: Python}

	cmd, err := launchers[Python].Command(context.Background(), configDir, root, "http://127.0.0.1:1234")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}

	if cmd.Dir != configDir {
		t.Errorf("Dir = %q, want %q", cmd.Dir, configDir)
	}
	if want := "./.ocel/discovery.py"; !slices.Contains(cmd.Args, want) {
		t.Errorf("Args = %q, want them to run %q", cmd.Args, want)
	}
	for _, want := range []string{"OCEL_PHASE=discovery", "OCEL_DEV_SERVER=http://127.0.0.1:1234", "PYTHONDONTWRITEBYTECODE=1"} {
		if !slices.Contains(cmd.Env, want) {
			t.Errorf("Env lacks %q", want)
		}
	}

	generated, err := os.ReadFile(filepath.Join(configDir, ".ocel", "discovery.py"))
	if err != nil {
		t.Fatalf("read the generated script: %v", err)
	}
	if !strings.Contains(string(generated), `import_module("infra")`) {
		t.Errorf("generated script = %q, want it to import the infra package", generated)
	}
}

func TestThePythonLauncherRunsFromTheNearestProjectFileAboveTheRoot(t *testing.T) {
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "pyproject.toml"), "")
	write(t, filepath.Join(configDir, "server", "requirements.txt"), "")
	write(t, filepath.Join(configDir, "server", "infra", "__init__.py"), "")

	cmd, err := launchers[Python].Command(context.Background(), configDir, Root{Dir: filepath.Join(configDir, "server", "infra"), Language: Python}, "http://127.0.0.1:1234")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if want := filepath.Join(configDir, "server"); cmd.Dir != want {
		t.Errorf("Dir = %q, want %q", cmd.Dir, want)
	}
}

func TestThePythonLauncherRefusesARootTooDeepToNameAPackage(t *testing.T) {
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "requirements.txt"), "")
	root := filepath.Join(configDir, "server", "infra")
	write(t, filepath.Join(root, "__init__.py"), "")

	_, err := launchers[Python].Command(context.Background(), configDir, Root{Dir: root, Language: Python}, "http://127.0.0.1:1234")
	if err == nil {
		t.Fatal("Command succeeded on a root nested under the project file, want error")
	}
	if !strings.Contains(err.Error(), root) {
		t.Errorf("error = %q, want it to name the root", err)
	}
}

func TestRunDeclaresWhatThePythonFixtureDeclares(t *testing.T) {
	configDir := repoFixture(t, filepath.Join("sdk", "python"))

	roots, err := Roots(configDir, nil)
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	if len(roots) != 1 || roots[0].Language != Python || roots[0].Dir != filepath.Join(configDir, "infra") {
		t.Fatalf("roots = %+v, want the python infra folder of the project", roots)
	}

	var mu sync.Mutex
	var declares []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Declare") {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode declare: %v", err)
			}
			mu.Lock()
			declares = append(declares, body)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	prepared, err := Prepare(configDir, roots)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	t.Setenv("PYTHONPATH", pythonSDKPath(t))

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), configDir, prepared, server.URL, &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v; stderr=%s", err, stderr.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(declares) != 1 {
		t.Fatalf("declares = %v, want exactly one", declares)
	}
	resource, _ := declares[0]["resource"].(map[string]any)
	if resource["name"] != "main" || resource["type"] != "LINK_TYPE_POSTGRES" {
		t.Errorf("resource = %v, want the postgres named main", resource)
	}
	source, _ := declares[0]["source"].(string)
	want := filepath.ToSlash(filepath.Join("infra", "__init__.py")) + ":3"
	if !strings.HasSuffix(filepath.ToSlash(source), want) {
		t.Errorf("source = %q, want it to end with %q", source, want)
	}
}

func pythonSDKPath(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "python", "ocel", "src"))
	if err != nil {
		t.Fatalf("locate the python sdk: %v", err)
	}
	return dir
}
