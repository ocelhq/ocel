package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func needsCargo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo is not on PATH")
	}
}

func rustFixture(t *testing.T, manifest string) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "Cargo.toml"), manifest)
	write(t, filepath.Join(root, "src", "main.rs"), "fn main() {}\n")
	write(t, filepath.Join(root, "infra", "mod.rs"), "pub const NAME: &str = \"main\";\n")
	return root
}

const rustBinManifest = "[package]\nname = \"web\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[workspace]\n"

func TestTheRustLauncherRunsTheCratesBinaryFromTheWorkspaceRoot(t *testing.T) {
	needsCargo(t)
	configDir := rustFixture(t, rustBinManifest)
	root := Root{Dir: filepath.Join(configDir, "infra"), Language: Rust}

	cmd, err := launchers[Rust].Command(context.Background(), configDir, root, "http://127.0.0.1:1234")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}

	if cmd.Dir != configDir {
		t.Errorf("Dir = %q, want the workspace root %q", cmd.Dir, configDir)
	}
	want := []string{"run", "--quiet", "--manifest-path", filepath.Join(configDir, "Cargo.toml"), "--bin", "web"}
	if !slices.Equal(cmd.Args[1:], want) || filepath.Base(cmd.Args[0]) != "cargo" {
		t.Errorf("Args = %q, want cargo %q", cmd.Args, want)
	}
	for _, env := range []string{"OCEL_PHASE=discovery", "OCEL_DEV_SERVER=http://127.0.0.1:1234", "OCEL_SOURCE_ROOT=" + configDir} {
		if !slices.Contains(cmd.Env, env) {
			t.Errorf("Env lacks %q", env)
		}
	}
}

func TestTheRustLauncherRunsTheCrateThatOwnsTheNearestCargoToml(t *testing.T) {
	needsCargo(t)
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "package.json"), "{}\n")
	crate := filepath.Join(configDir, "server")
	write(t, filepath.Join(crate, "Cargo.toml"), rustBinManifest)
	write(t, filepath.Join(crate, "src", "main.rs"), "fn main() {}\n")
	write(t, filepath.Join(crate, "infra", "mod.rs"), "pub const NAME: &str = \"main\";\n")

	cmd, err := launchers[Rust].Command(context.Background(), configDir, Root{Dir: filepath.Join(crate, "infra"), Language: Rust}, "http://127.0.0.1:1234")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if cmd.Dir != crate {
		t.Errorf("Dir = %q, want %q", cmd.Dir, crate)
	}
}

func TestTheRustLauncherRefusesARootWithNoCrateAboveIt(t *testing.T) {
	configDir := t.TempDir()
	root := filepath.Join(configDir, "infra")
	write(t, filepath.Join(root, "mod.rs"), "pub const NAME: &str = \"main\";\n")

	_, err := launchers[Rust].Command(context.Background(), configDir, Root{Dir: root, Language: Rust}, "http://127.0.0.1:1234")
	if err == nil {
		t.Fatal("Command succeeded with no Cargo.toml above the root, want an error")
	}
	if !strings.Contains(err.Error(), root) {
		t.Errorf("error = %q, want it to name the root", err)
	}
}

func TestTheRustLauncherRefusesACrateThatBuildsNoBinary(t *testing.T) {
	needsCargo(t)
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "Cargo.toml"), "[package]\nname = \"web\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[workspace]\n")
	write(t, filepath.Join(configDir, "src", "lib.rs"), "")
	root := filepath.Join(configDir, "infra")
	write(t, filepath.Join(root, "mod.rs"), "pub const NAME: &str = \"main\";\n")

	_, err := launchers[Rust].Command(context.Background(), configDir, Root{Dir: root, Language: Rust}, "http://127.0.0.1:1234")
	if err == nil {
		t.Fatal("Command succeeded on a crate with no binary, want an error")
	}
	if !strings.Contains(err.Error(), root) || !strings.Contains(err.Error(), "web") {
		t.Errorf("error = %q, want it to name the root and the crate", err)
	}
}

func TestRunDeclaresWhatTheRustFixtureDeclares(t *testing.T) {
	needsCargo(t)
	configDir := repoFixture(t, filepath.Join("sdk", "rust"))

	roots, err := Roots(configDir, nil)
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	if len(roots) != 1 || roots[0].Language != Rust || roots[0].Dir != filepath.Join(configDir, "infra") {
		t.Fatalf("roots = %+v, want the rust infra folder of the project", roots)
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
	colon := strings.LastIndex(source, ":")
	if colon <= 0 {
		t.Fatalf("source = %q, want a file and a line", source)
	}
	file, line := filepath.Clean(source[:colon]), source[colon+1:]
	if !filepath.IsAbs(file) {
		t.Errorf("source = %q, want the file as an absolute path", source)
	}
	rel, err := filepath.Rel(configDir, file)
	if err != nil {
		t.Fatalf("source = %q is not a file of the project at %s", source, configDir)
	}
	if want := filepath.Join("infra", "mod.rs"); rel != want || line != "1" {
		t.Errorf("source = %q, want %q line 1", source, want)
	}
}
