package discovery

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
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
	return root
}

const rustBinManifest = "[package]\nname = \"web\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[workspace]\n"

func TestTheRustLauncherRunsTheCratesBinaryFromTheWorkspaceRoot(t *testing.T) {
	needsCargo(t)
	configDir := rustFixture(t, rustBinManifest)
	root := Root{Dir: configDir, Language: Rust}

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

func TestTheRustLauncherRunsTheAppCrateAndNotTheProjectAroundIt(t *testing.T) {
	needsCargo(t)
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "package.json"), "{}\n")
	crate := filepath.Join(configDir, "server")
	write(t, filepath.Join(crate, "Cargo.toml"), rustBinManifest)
	write(t, filepath.Join(crate, "src", "main.rs"), "fn main() {}\n")

	cmd, err := launchers[Rust].Command(context.Background(), configDir, Root{Dir: crate, Language: Rust}, "http://127.0.0.1:1234")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if cmd.Dir != crate {
		t.Errorf("Dir = %q, want %q", cmd.Dir, crate)
	}
}

func TestTheRustLauncherRefusesADirThatIsNoCrate(t *testing.T) {
	needsCargo(t)
	configDir := t.TempDir()
	root := filepath.Join(configDir, "server")
	write(t, filepath.Join(root, "src", "main.rs"), "fn main() {}\n")

	_, err := launchers[Rust].Command(context.Background(), configDir, Root{Dir: root, Language: Rust}, "http://127.0.0.1:1234")
	if err == nil {
		t.Fatal("Command succeeded on a dir with no Cargo.toml, want an error")
	}
	if !strings.Contains(err.Error(), root) {
		t.Errorf("error = %q, want it to name the root", err)
	}
}

func TestTheRustLauncherRefusesACrateThatBuildsNoBinary(t *testing.T) {
	needsCargo(t)
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "Cargo.toml"), rustBinManifest)
	write(t, filepath.Join(configDir, "src", "lib.rs"), "")

	_, err := launchers[Rust].Command(context.Background(), configDir, Root{Dir: configDir, Language: Rust}, "http://127.0.0.1:1234")
	if err == nil {
		t.Fatal("Command succeeded on a crate with no binary, want an error")
	}
	if !strings.Contains(err.Error(), configDir) || !strings.Contains(err.Error(), "web") {
		t.Errorf("error = %q, want it to name the crate dir and the crate", err)
	}
}

func TestTheRustLauncherRefusesACrateThatBuildsSeveralBinaries(t *testing.T) {
	needsCargo(t)
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "Cargo.toml"), rustBinManifest+"\n[[bin]]\nname = \"web\"\npath = \"src/main.rs\"\n\n[[bin]]\nname = \"worker\"\npath = \"src/worker.rs\"\n")
	write(t, filepath.Join(configDir, "src", "main.rs"), "fn main() {}\n")
	write(t, filepath.Join(configDir, "src", "worker.rs"), "fn main() {}\n")

	_, err := launchers[Rust].Command(context.Background(), configDir, Root{Dir: configDir, Language: Rust}, "http://127.0.0.1:1234")
	if err == nil {
		t.Fatal("Command succeeded on a crate with two binaries, want an error")
	}
	want := "discovery: web builds 2 binaries, and ocel runs one binary per crate: keep one bin target in the crate at " + configDir
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestRunDeclaresWhatTheRustFixtureDeclares(t *testing.T) {
	needsCargo(t)
	configDir := repoFixture(t, filepath.Join("sdk", "rust"))

	roots, err := RootsOf(&projectconfig.Config{Dir: configDir, Apps: []projectconfig.App{{Name: "web", Path: "."}}})
	if err != nil {
		t.Fatalf("RootsOf: %v", err)
	}
	if len(roots) != 1 || roots[0].Language != Rust || roots[0].Dir != configDir {
		t.Fatalf("roots = %+v, want the app crate of the project", roots)
	}

	collected, url := declareCollector(t)

	prepared, err := Prepare(configDir, roots)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), configDir, prepared, url, &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v; stderr=%s", err, stderr.String())
	}

	declares := collected.declared()
	if len(declares) != 1 {
		t.Fatalf("declares = %v, want exactly one", declares)
	}
	resource := declares[0].GetResource()
	if resource.GetName() != "main" || resource.GetType() != linksv1.LinkType_LINK_TYPE_POSTGRES {
		t.Errorf("resource = %v, want the postgres named main", resource)
	}
	source := declares[0].GetSource()
	colon := strings.LastIndex(source, ":")
	if colon <= 0 {
		t.Fatalf("source = %q, want a file and a line", source)
	}
	file := filepath.Clean(source[:colon])
	if !filepath.IsAbs(file) {
		t.Errorf("source = %q, want the file as an absolute path", source)
	}
	rel, err := filepath.Rel(configDir, file)
	if err != nil {
		t.Fatalf("source = %q is not a file of the project at %s", source, configDir)
	}
	if want := filepath.Join("src", "main.rs"); rel != want {
		t.Errorf("source = %q, want it in %q", source, want)
	}
}

func TestRunDeclaresWhatASharedRustCrateDeclares(t *testing.T) {
	needsCargo(t)
	configDir := repoFixture(t, filepath.Join("sdk", "rust-workspace"))

	roots, err := RootsOf(&projectconfig.Config{Dir: configDir, Apps: []projectconfig.App{
		{Name: "api", Path: "./apps/api"},
		{Name: "web", Path: "./apps/web"},
	}})
	if err != nil {
		t.Fatalf("RootsOf: %v", err)
	}
	want := []Root{
		{Dir: filepath.Join(configDir, "apps", "api"), Language: Rust},
		{Dir: filepath.Join(configDir, "apps", "web"), Language: Rust},
	}
	if !slices.Equal(roots, want) {
		t.Fatalf("roots = %+v, want the two app crates %+v", roots, want)
	}

	collected, url := declareCollector(t)

	prepared, err := Prepare(configDir, roots)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), configDir, prepared, url, &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v; stderr=%s", err, stderr.String())
	}

	shared := filepath.Join(configDir, "crates", "infra", "src", "lib.rs")

	declares := collected.declared()
	if len(declares) != 2 {
		t.Fatalf("declares = %v, want one per app binary", declares)
	}
	for _, declared := range declares {
		resource := declared.GetResource()
		if resource.GetName() != "main" || resource.GetType() != linksv1.LinkType_LINK_TYPE_POSTGRES {
			t.Errorf("resource = %v, want the postgres named main", resource)
		}
		if file := sourceFile(t, declared.GetSource()); file != shared {
			t.Errorf("source = %q, want the declaration in %q", declared.GetSource(), shared)
		}
	}
	if a, b := declares[0].GetSource(), declares[1].GetSource(); a != b {
		t.Errorf("sources = %q and %q, want both binaries to name the same declaration", a, b)
	}

	variables := collected.declaredVariables()
	if len(variables) != 2 {
		t.Fatalf("variables = %v, want one per app binary", variables)
	}
	for _, variable := range variables {
		if variable.GetKey() != "GREETING" {
			t.Errorf("variable = %v, want the key GREETING", variable)
		}
		if file := sourceFile(t, variable.GetSource()); file != shared {
			t.Errorf("source = %q, want the declaration in %q", variable.GetSource(), shared)
		}
	}
	if a, b := variables[0].GetSource(), variables[1].GetSource(); a != b {
		t.Errorf("sources = %q and %q, want both binaries to name the same declaration", a, b)
	}
}

func sourceFile(t *testing.T, source string) string {
	t.Helper()
	colon := strings.LastIndex(source, ":")
	if colon <= 0 {
		t.Fatalf("source = %q, want a file and a line", source)
	}
	file := filepath.Clean(source[:colon])
	if !filepath.IsAbs(file) {
		t.Errorf("source = %q, want the file as an absolute path", source)
	}
	return file
}
