package discovery

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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

const markedMain = "fn main() {\n    let _ = std::hint::black_box(b\"" + ocelMainMarker + "\");\n}\n"

func TestRustDiscoveryRunsTheCratesBinaryFromTheWorkspaceRoot(t *testing.T) {
	needsCargo(t)
	configDir := rustFixture(t, rustBinManifest)
	write(t, filepath.Join(configDir, "src", "main.rs"), markedMain)
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

const refusesTheDevServerBuildScript = "fn main() {\n    for name in [\"" + processenv.DevServerEnvVar + "\", \"" + processenv.DevServerTokenEnvVar + "\"] {\n        if std::env::var_os(name).is_some() {\n            panic!(\"the build sees {name}\");\n        }\n    }\n}\n"

func TestRustDiscoveryBuildsWithoutTheDevServersAddressOrTokenAndRunsWithoutRebuilding(t *testing.T) {
	needsCargo(t)
	configDir := rustFixture(t, rustBinManifest)
	write(t, filepath.Join(configDir, "src", "main.rs"), markedMain)
	write(t, filepath.Join(configDir, "build.rs"), refusesTheDevServerBuildScript)

	cmd, err := rustCommand(context.Background(), configDir, Root{Dir: configDir, Language: language.Rust}, testServer)
	if err != nil {
		t.Fatalf("Command = %v, want the build to see neither the dev server's address nor its token", err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cargo run with the discovery environment: %v: %s, want the built binary run without rebuilding", err, out)
	}
}

func TestRustDiscoveryRunsTheAppCrateAndNotTheProjectAroundIt(t *testing.T) {
	needsCargo(t)
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "package.json"), "{}\n")
	crate := filepath.Join(configDir, "server")
	write(t, filepath.Join(crate, "Cargo.toml"), rustBinManifest)
	write(t, filepath.Join(crate, "src", "main.rs"), "fn main() {}\n")

	binary, err := findRustBinary(context.Background(), Root{Dir: crate, Language: language.Rust})
	if err != nil {
		t.Fatalf("findRustBinary: %v", err)
	}
	if binary.workspace.Root != crate {
		t.Errorf("workspace root = %q, want %q", binary.workspace.Root, crate)
	}
}

func TestRustDiscoveryRefusesADirThatIsNoCrate(t *testing.T) {
	needsCargo(t)
	configDir := t.TempDir()
	root := filepath.Join(configDir, "server")
	write(t, filepath.Join(root, "src", "main.rs"), "fn main() {}\n")

	_, err := findRustBinary(context.Background(), Root{Dir: root, Language: language.Rust})
	if err == nil {
		t.Fatal("findRustBinary succeeded on a dir with no Cargo.toml, want an error")
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

	_, err := findRustBinary(context.Background(), Root{Dir: configDir, Language: language.Rust})
	if err == nil {
		t.Fatal("findRustBinary succeeded on a crate with no binary, want an error")
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

	_, err := findRustBinary(context.Background(), Root{Dir: configDir, Language: language.Rust})
	if err == nil {
		t.Fatal("findRustBinary succeeded on a crate with two binaries, want an error")
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

	binary, err := findRustBinary(context.Background(), Root{Dir: configDir, Language: language.Rust, Bin: "worker"})
	if err != nil {
		t.Fatalf("findRustBinary: %v", err)
	}
	if binary.bin.Name != "worker" {
		t.Errorf("binary = %q, want worker", binary.bin.Name)
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

			_, err := findRustBinary(context.Background(), root)
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

	_, err := findRustBinary(context.Background(), Root{Dir: configDir, Language: language.Rust, Bin: "worker"})
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

	prepared, err := Prepare(context.Background(), configDir, roots, io.Discard)
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

	prepared, err := Prepare(context.Background(), configDir, roots, io.Discard)
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

	binary, err := findRustBinary(context.Background(), Root{Dir: link, Language: language.Rust})
	if err != nil {
		t.Fatalf("findRustBinary: %v", err)
	}
	if binary.bin.Name != "web" {
		t.Errorf("binary = %q, want the crate's binary web", binary.bin.Name)
	}
}

func TestRustDiscoveryRunsTheBinaryTheCrateRunsByDefault(t *testing.T) {
	needsCargo(t)
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "Cargo.toml"), "[package]\nname = \"web\"\nversion = \"0.1.0\"\nedition = \"2021\"\ndefault-run = \"worker\"\n\n[workspace]\n\n[[bin]]\nname = \"web\"\npath = \"src/main.rs\"\n\n[[bin]]\nname = \"worker\"\npath = \"src/worker.rs\"\n")
	write(t, filepath.Join(configDir, "src", "main.rs"), "fn main() {}\n")
	write(t, filepath.Join(configDir, "src", "worker.rs"), "fn main() {}\n")

	binary, err := findRustBinary(context.Background(), Root{Dir: configDir, Language: language.Rust})
	if err != nil {
		t.Fatalf("findRustBinary: %v", err)
	}
	if binary.bin.Name != "worker" {
		t.Errorf("binary = %q, want worker, the binary default-run names", binary.bin.Name)
	}
}

const servesForever = "use std::net::TcpListener;\n\nfn main() {\n    let listener = TcpListener::bind(\"127.0.0.1:0\").expect(\"bind\");\n    for stream in listener.incoming() {\n        drop(stream);\n    }\n}\n"

func TestRunRefusesARustBinaryWithoutOcelMainInsteadOfStartingTheApp(t *testing.T) {
	needsCargo(t)
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "Cargo.toml"), rustBinManifest)
	write(t, filepath.Join(configDir, "src", "main.rs"), servesForever)
	mainFile, err := filepath.EvalSymlinks(filepath.Join(configDir, "src", "main.rs"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := Prepare(context.Background(), configDir, []Root{{Dir: configDir, Language: language.Rust}}, io.Discard)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	err = Run(ctx, configDir, prepared, okServer(t), &stdout, &stderr)

	if err == nil {
		t.Fatal("Run succeeded, want a refusal: the binary has no #[ocel::main]")
	}
	for _, want := range []string{"#[ocel::main]", "ocel::discover()", `"web"`, filepath.Dir(filepath.Dir(mainFile)), mainFile} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %s", err, want)
		}
	}
}

func TestRunShowsTheWarningsOfARustBinaryOnce(t *testing.T) {
	needsCargo(t)
	configDir := rustFixture(t, rustBinManifest)
	write(t, filepath.Join(configDir, "src", "main.rs"), strings.Replace(markedMain, "fn main() {\n", "fn main() {\n    let unread = 1;\n", 1))
	prepared, err := Prepare(context.Background(), configDir, []Root{{Dir: configDir, Language: language.Rust}}, io.Discard)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), configDir, prepared, okServer(t), &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v; stderr=%s", err, stderr.String())
	}
	if n := strings.Count(stderr.String(), "unused variable"); n != 1 {
		t.Errorf("stderr = %q, want the warning once", stderr.String())
	}
}

func reuseTheRustFixturesSDKBuild(t *testing.T) {
	t.Helper()
	t.Setenv("CARGO_TARGET_DIR", filepath.Join(repoFixture(t, "rust"), "target"))
}

func rustSDKApp(t *testing.T, editMain func(main string) string) string {
	t.Helper()
	fixture := repoFixture(t, "rust")
	app, err := os.ReadFile(filepath.Join(fixture, "src", "main.rs"))
	if err != nil {
		t.Fatalf("read the fixture app: %v", err)
	}
	edited := editMain(string(app))
	if edited == string(app) {
		t.Fatal("the fixture app's main was left as it is")
	}
	lock, err := os.ReadFile(filepath.Join(fixture, "Cargo.lock"))
	if err != nil {
		t.Fatalf("read the fixture lock: %v", err)
	}
	sdk, err := filepath.Abs(filepath.Join("..", "..", "..", "crates", "ocel-sdk"))
	if err != nil {
		t.Fatal(err)
	}
	configDir := t.TempDir()
	write(t, filepath.Join(configDir, "Cargo.toml"), "[package]\nname = \"api\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[workspace]\n\n[dependencies]\nocel-sdk = { path = "+strconv.Quote(sdk)+" }\n")
	write(t, filepath.Join(configDir, "Cargo.lock"), strings.Replace(string(lock), "name = \"web\"", "name = \"api\"", 1))
	write(t, filepath.Join(configDir, "src", "main.rs"), edited)
	return configDir
}

func TestRunRefusesARustBinaryThatLinksTheSDKWithoutOcelMain(t *testing.T) {
	needsCargo(t)
	configDir := rustSDKApp(t, func(main string) string {
		return strings.Replace(main, "#[ocel::main]\n", "", 1)
	})
	reuseTheRustFixturesSDKBuild(t)
	prepared, err := Prepare(context.Background(), configDir, []Root{{Dir: configDir, Language: language.Rust}}, io.Discard)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var stdout, stderr bytes.Buffer
	err = Run(ctx, configDir, prepared, okServer(t), &stdout, &stderr)

	if err == nil || !strings.Contains(err.Error(), "#[ocel::main]") || !strings.Contains(err.Error(), `"api"`) {
		t.Fatalf("Run = %v, want a refusal asking the binary api for #[ocel::main]; stderr=%s", err, stderr.String())
	}
}

func TestRustDiscoveryRefusesABinaryThatCallsOnlyOcelServeWorker(t *testing.T) {
	needsCargo(t)
	configDir := rustSDKApp(t, func(main string) string {
		return strings.Replace(main, "#[ocel::main]\nfn main() {\n", "fn main() {\n    if ocel::serve_worker().expect(\"ocel worker\") {\n        return;\n    }\n", 1)
	})
	reuseTheRustFixturesSDKBuild(t)

	_, err := rustCommand(context.Background(), configDir, Root{Dir: configDir, Language: language.Rust}, testServer)
	if err == nil || !strings.Contains(err.Error(), "ocel::discover()") || !strings.Contains(err.Error(), `"api"`) {
		t.Fatalf("Command = %v, want a refusal: serve_worker alone never declares, so the binary would start the app", err)
	}
}

func TestRunDeclaresWhatARustBinaryThatCallsOcelDiscoverItselfDeclares(t *testing.T) {
	needsCargo(t)
	configDir := rustSDKApp(t, func(main string) string {
		return strings.Replace(main, "#[ocel::main]\nfn main() {\n", "fn main() {\n    if ocel::discover().expect(\"ocel discovery\") {\n        return;\n    }\n", 1)
	})
	reuseTheRustFixturesSDKBuild(t)
	prepared, err := Prepare(context.Background(), configDir, []Root{{Dir: configDir, Language: language.Rust}}, io.Discard)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	collected, server := declareCollector(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var stdout, stderr bytes.Buffer
	if err := Run(ctx, configDir, prepared, server, &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v; stderr=%s", err, stderr.String())
	}
	if declares := collected.declared(); len(declares) != 2 {
		t.Errorf("declares = %v, want the postgres and the bucket the app declares", declares)
	}
}

func TestOcelMainMarkerIsTheOneTheSDKDefines(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "crates", "ocel-sdk", "src", "lib.rs"))
	if err != nil {
		t.Fatalf("read the SDK: %v", err)
	}
	defined := regexp.MustCompile(`pub\(crate\) const MAIN_MARKER: &\[u8\] = b"([^"]+)";`).FindSubmatch(source)
	if defined == nil {
		t.Fatal("the SDK defines no MAIN_MARKER")
	}
	if string(defined[1]) != ocelMainMarker {
		t.Errorf("ocelMainMarker = %q, want the SDK's MAIN_MARKER %q", ocelMainMarker, defined[1])
	}
}

func TestHasOcelMainMarkerFindsTheMarkerWhereverItLies(t *testing.T) {
	marker := []byte(ocelMainMarker)
	for _, tt := range []struct {
		name     string
		contents []byte
		want     bool
	}{
		{"none", bytes.Repeat([]byte{0}, 3<<20), false},
		{"a prefix of it at the end", append(bytes.Repeat([]byte{0}, 1<<20), marker[:len(marker)-1]...), false},
		{"at the start", append(slices.Clone(marker), bytes.Repeat([]byte{0}, 3<<20)...), true},
		{"across the end of the first read", append(bytes.Repeat([]byte{0}, 1<<20-len(marker)/2), marker...), true},
		{"at the end", append(bytes.Repeat([]byte{0}, 3<<20), marker...), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "web")
			if err := os.WriteFile(path, tt.contents, 0o755); err != nil {
				t.Fatal(err)
			}
			got, err := hasOcelMainMarker(path)
			if err != nil || got != tt.want {
				t.Errorf("hasOcelMainMarker = %v, %v, want %v", got, err, tt.want)
			}
		})
	}
}
