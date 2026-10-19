package discovery

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/processenv"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
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

func TestRustDiscoveryRunsTheCratesBinaryFromTheWorkspaceRoot(t *testing.T) {
	needsCargo(t)
	configDir := rustFixture(t, rustBinManifest)
	root := Root{Dir: configDir, Language: language.Rust}

	cmd, err := rustCommand(context.Background(), configDir, root, testServer)
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
	for _, env := range []string{processenv.PhaseEnvVar + "=discovery", processenv.DevServerEnvVar + "=http://127.0.0.1:1234", processenv.DevServerTokenEnvVar + "=opensesame", "OCEL_SOURCE_ROOT=" + configDir} {
		if !slices.Contains(cmd.Env, env) {
			t.Errorf("Env lacks %q", env)
		}
	}
}

func TestRustDiscoveryRunsTheAppCrateAndNotTheProjectAroundIt(t *testing.T) {
	needsCargo(t)
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "package.json"), "{}\n")
	crate := filepath.Join(configDir, "server")
	write(t, filepath.Join(crate, "Cargo.toml"), rustBinManifest)
	write(t, filepath.Join(crate, "src", "main.rs"), "fn main() {}\n")

	cmd, err := rustCommand(context.Background(), configDir, Root{Dir: crate, Language: language.Rust}, testServer)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if cmd.Dir != crate {
		t.Errorf("Dir = %q, want %q", cmd.Dir, crate)
	}
}

func TestRustDiscoveryRefusesADirThatIsNoCrate(t *testing.T) {
	needsCargo(t)
	configDir := t.TempDir()
	root := filepath.Join(configDir, "server")
	write(t, filepath.Join(root, "src", "main.rs"), "fn main() {}\n")

	_, err := rustCommand(context.Background(), configDir, Root{Dir: root, Language: language.Rust}, testServer)
	if err == nil {
		t.Fatal("Command succeeded on a dir with no Cargo.toml, want an error")
	}
	if !strings.Contains(err.Error(), root) {
		t.Errorf("error = %q, want it to name the root", err)
	}
}

func TestRustDiscoveryRefusesACrateThatBuildsNoBinary(t *testing.T) {
	needsCargo(t)
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "Cargo.toml"), rustBinManifest)
	write(t, filepath.Join(configDir, "src", "lib.rs"), "")

	_, err := rustCommand(context.Background(), configDir, Root{Dir: configDir, Language: language.Rust}, testServer)
	if err == nil {
		t.Fatal("Command succeeded on a crate with no binary, want an error")
	}
	if !strings.Contains(err.Error(), configDir) || !strings.Contains(err.Error(), "web") {
		t.Errorf("error = %q, want it to name the crate dir and the crate", err)
	}
}

func TestRustDiscoveryRefusesACrateThatBuildsSeveralBinaries(t *testing.T) {
	needsCargo(t)
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "Cargo.toml"), rustBinManifest+"\n[[bin]]\nname = \"web\"\npath = \"src/main.rs\"\n\n[[bin]]\nname = \"worker\"\npath = \"src/worker.rs\"\n")
	write(t, filepath.Join(configDir, "src", "main.rs"), "fn main() {}\n")
	write(t, filepath.Join(configDir, "src", "worker.rs"), "fn main() {}\n")

	_, err := rustCommand(context.Background(), configDir, Root{Dir: configDir, Language: language.Rust}, testServer)
	if err == nil {
		t.Fatal("Command succeeded on a crate with two binaries, want an error")
	}
	for _, want := range []string{configDir, `"web"`, `"worker"`, `"entrypoint"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %s", err, want)
		}
	}
}

func TestRustDiscoveryRunsTheBinaryTheAppsEntrypointNames(t *testing.T) {
	needsCargo(t)
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "Cargo.toml"), rustBinManifest+"\n[[bin]]\nname = \"web\"\npath = \"src/main.rs\"\n\n[[bin]]\nname = \"worker\"\npath = \"src/worker.rs\"\n")
	write(t, filepath.Join(configDir, "src", "main.rs"), "fn main() {}\n")
	write(t, filepath.Join(configDir, "src", "worker.rs"), "fn main() {}\n")

	cmd, err := rustCommand(context.Background(), configDir, Root{Dir: configDir, Language: language.Rust, Bin: "worker"}, testServer)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if want := []string{"--bin", "worker"}; !slices.Equal(cmd.Args[len(cmd.Args)-2:], want) {
		t.Errorf("Args = %q, want them to end %q", cmd.Args, want)
	}
}

func TestRustDiscoveryRefusesTwoAppsOnOneCrateWhereTheOneNamingNoBinaryRunsAnother(t *testing.T) {
	needsCargo(t)
	bins := "\n[[bin]]\nname = \"web\"\npath = \"src/main.rs\"\n\n[[bin]]\nname = \"worker\"\npath = \"src/worker.rs\"\n"
	shared := Root{Language: language.Rust, Bin: "worker", BinApp: "jobs", DefaultBinApp: "web"}

	for _, tt := range []struct {
		name, manifest string
		refused        bool
	}{
		{"the crate runs web by default", strings.Replace(rustBinManifest, "[package]\n", "[package]\ndefault-run = \"web\"\n", 1) + bins, true},
		{"the crate runs no one binary by default", rustBinManifest + bins, true},
		{"the crate runs worker by default", strings.Replace(rustBinManifest, "[package]\n", "[package]\ndefault-run = \"worker\"\n", 1) + bins, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			configDir := t.TempDir()
			write(t, filepath.Join(configDir, "Cargo.toml"), tt.manifest)
			write(t, filepath.Join(configDir, "src", "main.rs"), "fn main() {}\n")
			write(t, filepath.Join(configDir, "src", "worker.rs"), "fn main() {}\n")
			root := shared
			root.Dir = configDir

			_, err := rustCommand(context.Background(), configDir, root, testServer)
			if tt.refused && (err == nil || !strings.Contains(err.Error(), `"jobs"`) || !strings.Contains(err.Error(), `"web"`)) {
				t.Errorf("Command = %v, want jobs and web refused: they run different binaries of one crate", err)
			}
			if !tt.refused && err != nil {
				t.Errorf("Command = %v, want worker run: web runs it too, as the crate's default", err)
			}
		})
	}
}

func TestRustDiscoveryRefusesAnEntrypointNamingNoBinaryOfTheCrate(t *testing.T) {
	needsCargo(t)
	configDir := rustFixture(t, rustBinManifest)

	_, err := rustCommand(context.Background(), configDir, Root{Dir: configDir, Language: language.Rust, Bin: "worker"}, testServer)
	if err == nil || !strings.Contains(err.Error(), `"worker"`) || !strings.Contains(err.Error(), `"web"`) {
		t.Fatalf("error = %v, want a refusal naming the entrypoint worker and the binary web the crate builds", err)
	}
}

func TestRunDeclaresWhatTheRustFixtureDeclares(t *testing.T) {
	needsCargo(t)
	configDir := repoFixture(t, "rust")

	roots, err := RootsOf(&project.Project{Dir: configDir, Apps: []project.App{{Name: "web", Path: "."}}})
	if err != nil {
		t.Fatalf("RootsOf: %v", err)
	}
	if len(roots) != 1 || roots[0].Language != language.Rust || roots[0].Dir != configDir {
		t.Fatalf("roots = %+v, want the app crate of the project", roots)
	}

	collected, server := declareCollector(t)

	prepared, err := Prepare(configDir, roots)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), configDir, prepared, server, &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v; stderr=%s", err, stderr.String())
	}

	declares := collected.declared()
	if len(declares) != 2 {
		t.Fatalf("declares = %v, want the postgres and the bucket the fixture declares", declares)
	}
	declared := map[resourcesv1.ResourceType]*resourcesv1.DeclareRequest{}
	for _, request := range declares {
		declared[request.GetResource().GetType()] = request
	}
	for kind, name := range map[resourcesv1.ResourceType]string{
		resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES: "main",
		resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET:   "uploads",
	} {
		request, found := declared[kind]
		if !found {
			t.Fatalf("declares = %v, want one of %v", declares, kind)
		}
		if request.GetResource().GetName() != name {
			t.Errorf("resource = %v, want %q", request.GetResource(), name)
		}
	}

	source := declared[resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES].GetSource()
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
	configDir := repoFixture(t, "rust-workspace")

	roots, err := RootsOf(&project.Project{Dir: configDir, Apps: []project.App{
		{Name: "api", Path: "./apps/api"},
		{Name: "web", Path: "./apps/web"},
	}})
	if err != nil {
		t.Fatalf("RootsOf: %v", err)
	}
	want := []Root{
		{Dir: filepath.Join(configDir, "apps", "api"), Language: language.Rust},
		{Dir: filepath.Join(configDir, "apps", "web"), Language: language.Rust},
	}
	if !slices.Equal(roots, want) {
		t.Fatalf("roots = %+v, want the two app crates %+v", roots, want)
	}

	collected, server := declareCollector(t)

	prepared, err := Prepare(configDir, roots)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), configDir, prepared, server, &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v; stderr=%s", err, stderr.String())
	}

	shared := filepath.Join(configDir, "crates", "infra", "src", "lib.rs")

	declares := collected.declared()
	if len(declares) != 2 {
		t.Fatalf("declares = %v, want one per app binary", declares)
	}
	for _, declared := range declares {
		resource := declared.GetResource()
		if resource.GetName() != "main" || resource.GetType() != resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES {
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

func TestRustDiscoveryRunsACrateReachedThroughASymlink(t *testing.T) {
	needsCargo(t)
	crate := rustFixture(t, rustBinManifest)
	link := filepath.Join(t.TempDir(), "server")
	if err := os.Symlink(crate, link); err != nil {
		t.Fatal(err)
	}

	cmd, err := rustCommand(context.Background(), link, Root{Dir: link, Language: language.Rust}, testServer)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if want := []string{"--bin", "web"}; !slices.Equal(cmd.Args[len(cmd.Args)-2:], want) {
		t.Errorf("Args = %q, want the crate's binary %q", cmd.Args, want)
	}
}

func TestRustDiscoveryRunsTheBinaryTheCrateRunsByDefault(t *testing.T) {
	needsCargo(t)
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "Cargo.toml"), "[package]\nname = \"web\"\nversion = \"0.1.0\"\nedition = \"2021\"\ndefault-run = \"worker\"\n\n[workspace]\n\n[[bin]]\nname = \"web\"\npath = \"src/main.rs\"\n\n[[bin]]\nname = \"worker\"\npath = \"src/worker.rs\"\n")
	write(t, filepath.Join(configDir, "src", "main.rs"), "fn main() {}\n")
	write(t, filepath.Join(configDir, "src", "worker.rs"), "fn main() {}\n")

	cmd, err := rustCommand(context.Background(), configDir, Root{Dir: configDir, Language: language.Rust}, testServer)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if want := []string{"--bin", "worker"}; !slices.Equal(cmd.Args[len(cmd.Args)-2:], want) {
		t.Errorf("Args = %q, want them to end %q — the binary default-run names", cmd.Args, want)
	}
}
