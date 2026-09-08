package discovery

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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
	for _, env := range []string{"OCEL_PHASE=discovery", "OCEL_DEV_SERVER=http://127.0.0.1:1234"} {
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
