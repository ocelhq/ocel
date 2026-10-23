package workspace_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/workspace"
)

const webCrate = "[package]\nname = \"web\"\nversion = \"0.1.0\"\n\n[workspace]\n\n"

func TestARustAppIsBuiltFromTheDirectoryHoldingItAndEveryCrateItReachesByPath(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files map[string]string
		root  string
		path  string
	}{
		{
			name: "a path dependency",
			files: map[string]string{
				"tests/fixtures/web/Cargo.toml": webCrate + "[dependencies]\nocel-sdk = { path = \"../../../crates/ocel-sdk\", features = [\"axum\"] }\nserde = \"1\"\n",
				"crates/ocel-sdk/Cargo.toml":    "[package]\nname = \"ocel-sdk\"\nversion = \"0.1.0\"\n",
			},
			root: ".",
			path: "tests/fixtures/web",
		},
		{
			name: "a path dependency of a path dependency",
			files: map[string]string{
				"apps/web/Cargo.toml":      webCrate + "[dependencies]\nsdk = { path = \"../sdk\" }\n",
				"apps/sdk/Cargo.toml":      "[package]\nname = \"sdk\"\nversion = \"0.1.0\"\n\n[dependencies]\nmacros = { path = \"../../vendor/macros\" }\n",
				"vendor/macros/Cargo.toml": "[package]\nname = \"macros\"\nversion = \"0.1.0\"\n",
			},
			root: ".",
			path: "apps/web",
		},
		{
			name: "the workspace a path dependency inherits its fields from",
			files: map[string]string{
				"Cargo.toml":          "[workspace]\nmembers = [\"apps/sdk\"]\n\n[workspace.package]\nversion = \"0.1.0\"\n",
				"apps/web/Cargo.toml": webCrate + "[dependencies]\nsdk = { path = \"../sdk\" }\n",
				"apps/sdk/Cargo.toml": "[package]\nname = \"sdk\"\nversion.workspace = true\n",
			},
			root: ".",
			path: "apps/web",
		},
		{
			name: "a dev, build and target-specific dependency, and a patch",
			files: map[string]string{
				"apps/web/Cargo.toml": webCrate +
					"[dev-dependencies]\ntesting = { path = \"../../dev/testing\" }\n\n" +
					"[build-dependencies]\nbuild = { path = \"../../build/helper\" }\n\n" +
					"[target.'cfg(unix)'.dependencies]\nunix = { path = \"../../unix/shim\" }\n\n" +
					"[patch.crates-io]\nserde = { path = \"../../patched/serde\" }\n",
			},
			root: ".",
			path: "apps/web",
		},
		{
			name: "only registry and git dependencies",
			files: map[string]string{
				"apps/web/Cargo.toml": webCrate + "[dependencies]\nserde = \"1\"\naxum = { version = \"0.8\" }\nfork = { git = \"https://example.com/fork.git\" }\n",
			},
			root: "apps/web",
			path: ".",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.files[".git/HEAD"] = "ref: refs/heads/main\n"
			write(t, dir, tt.files)
			app := filepath.Join(dir, "apps", "web")
			if _, ok := tt.files["tests/fixtures/web/Cargo.toml"]; ok {
				app = filepath.Join(dir, "tests", "fixtures", "web")
			}

			got := locatedIn(t, dir, app, language.Rust)

			if want := filepath.Join(dir, filepath.FromSlash(tt.root)); got.Root != want || got.Path != tt.path {
				t.Errorf("Locate() = %+v, want the context %s with the app at %s — cargo build in the image reads every manifest the crate reaches by path", got, want, tt.path)
			}
		})
	}
}

func TestARustAppBuiltFromAJSWorkspaceRootIsNoMemberOfThatWorkspace(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		".git/HEAD":                           "ref: refs/heads/main\n",
		"pnpm-workspace.yaml":                 "packages:\n  - tests/fixtures/*/*\n",
		"pnpm-lock.yaml":                      "lockfileVersion: '9.0'\n",
		"package.json":                        `{"name":"root"}`,
		"crates/ocel-sdk/Cargo.toml":          "[package]\nname = \"ocel-sdk\"\nversion = \"0.1.0\"\n",
		"tests/fixtures/sdk/rust/Cargo.toml":  webCrate + "[dependencies]\nocel-sdk = { path = \"../../../../crates/ocel-sdk\" }\n",
		"tests/fixtures/sdk/rust/src/main.rs": "fn main() {}\n",
	})

	loc := locatedIn(t, dir, filepath.Join(dir, "tests", "fixtures", "sdk", "rust"), language.Rust)

	if loc.Root != dir || loc.Member {
		t.Errorf("Locate() = %+v, want the context %s and no workspace membership — the pnpm globs that cover the crate say nothing about how cargo builds it", loc, dir)
	}
	commands, err := loc.Commands()
	if err != nil || commands.Start != "./tests/fixtures/sdk/rust/bin/web" {
		t.Errorf("Commands() = %+v, %v, want the crate's binary started rather than a workspace member's start script", commands, err)
	}
}

func TestARustAppReachingACrateOutsideTheRepositoryIsRefusedRatherThanBuiltFromAboveIt(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"elsewhere/sdk/Cargo.toml": "[package]\nname = \"sdk\"\nversion = \"0.1.0\"\n",
		"repo/.git/HEAD":           "ref: refs/heads/main\n",
		"repo/web/Cargo.toml":      webCrate + "[dependencies]\nsdk = { path = \"../../elsewhere/sdk\" }\n",
	})

	_, err := locate(t, filepath.Join(dir, "repo"), filepath.Join(dir, "repo", "web"), language.Rust)
	if err == nil {
		t.Fatal("Locate() took a context above the repository the app is checked out in, so the image would be built from whatever else sits beside it")
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, "elsewhere", "sdk")) || !strings.Contains(err.Error(), filepath.Join(dir, "repo")) {
		t.Errorf("Locate() = %v, and the reader is never told which crate lies outside which repository", err)
	}
}

func TestARustAppBelowItsContextBuildsTheOneBinaryItsCrateHasWithEverythingItReachesInTheBuildStep(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		".git/HEAD":                      "ref: refs/heads/main\n",
		"crates/ocel-sdk/Cargo.toml":     "[package]\nname = \"ocel-sdk\"\nversion = \"0.1.0\"\n",
		"tests/fixtures/web/Cargo.toml":  webCrate + "[dependencies]\nocel-sdk = { path = \"../../../crates/ocel-sdk\" }\n",
		"tests/fixtures/web/src/main.rs": "fn main() {}\n",
	})

	commands, err := locatedIn(t, dir, filepath.Join(dir, "tests", "fixtures", "web"), language.Rust).Commands()
	if err != nil {
		t.Fatalf("Commands() = %v", err)
	}
	want := workspace.Commands{
		Build:           "cd tests/fixtures/web && mkdir -p bin && cargo build --release --bin web --target-dir ../../../target && cp ../../../target/release/web bin/",
		Start:           "./tests/fixtures/web/bin/web",
		InstallsInBuild: true,
	}
	if !reflect.DeepEqual(commands, want) {
		t.Errorf("Commands() = %+v, want %+v — no Cargo.toml sits at the context root for railpack to build, so the build step compiles the app's one binary into the target the image leaves out", commands, want)
	}
}

func TestARustAppBelowItsContextBuildsTheBinaryItsCrateRunsByDefault(t *testing.T) {
	for _, tt := range []struct {
		name     string
		manifest string
		bin      string
	}{
		{
			name:     "the one binary declared by name",
			manifest: "[package]\nname = \"web\"\nversion = \"0.1.0\"\nautobins = false\n\n[[bin]]\nname = \"api\"\npath = \"src/main.rs\"\n\n",
			bin:      "api",
		},
		{
			name:     "a default-run among several",
			manifest: "[package]\nname = \"web\"\nversion = \"0.1.0\"\ndefault-run = \"worker\"\n\n",
			bin:      "worker",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, map[string]string{
				".git/HEAD":             "ref: refs/heads/main\n",
				"sdk/Cargo.toml":        "[package]\nname = \"sdk\"\nversion = \"0.1.0\"\n",
				"web/Cargo.toml":        tt.manifest + "[workspace]\n\n[dependencies]\nsdk = { path = \"../sdk\" }\n",
				"web/src/main.rs":       "fn main() {}\n",
				"web/src/bin/worker.rs": "fn main() {}\n",
			})

			commands, err := locatedIn(t, dir, filepath.Join(dir, "web"), language.Rust).Commands()
			if err != nil {
				t.Fatalf("Commands() = %v", err)
			}
			if want := "./web/bin/" + tt.bin; commands.Start != want {
				t.Errorf("Commands().Start = %q, want %q", commands.Start, want)
			}
		})
	}
}

func TestARustAppBelowItsContextBuildingSeveralBinariesAndNamingNoneIsRefusedWithTheFieldToSet(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		".git/HEAD":             "ref: refs/heads/main\n",
		"sdk/Cargo.toml":        "[package]\nname = \"sdk\"\nversion = \"0.1.0\"\n",
		"web/Cargo.toml":        webCrate + "[dependencies]\nsdk = { path = \"../sdk\" }\n",
		"web/src/main.rs":       "fn main() {}\n",
		"web/src/bin/worker.rs": "fn main() {}\n",
	})

	_, err := locatedIn(t, dir, filepath.Join(dir, "web"), language.Rust).Commands()
	if err == nil || !strings.Contains(err.Error(), `"entrypoint"`) || !strings.Contains(err.Error(), "worker") {
		t.Errorf("Commands() = %v, want a refusal naming the binaries and the entrypoint that picks one", err)
	}
}

func TestARustWorkspaceMemberNamingNoEntrypointBuildsItsOwnBinaryRatherThanAnotherMembers(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files map[string]string
		app   string
	}{
		{
			name: "a member of the workspace above it",
			files: map[string]string{
				"Cargo.toml":           "[workspace]\nmembers = [\"crates/a\", \"crates/b\"]\n",
				"crates/a/Cargo.toml":  "[package]\nname = \"a\"\nversion = \"0.1.0\"\n",
				"crates/a/src/main.rs": "fn main() {}\n",
				"crates/b/Cargo.toml":  "[package]\nname = \"b\"\nversion = \"0.1.0\"\n",
				"crates/b/src/main.rs": "fn main() {}\n",
			},
			app: "crates/b",
		},
		{
			name: "a crate of its own depending on a member",
			files: map[string]string{
				"Cargo.toml":             "[workspace]\nmembers = [\"crates/*\"]\n",
				"crates/cli/Cargo.toml":  "[package]\nname = \"cli\"\nversion = \"0.1.0\"\n",
				"crates/cli/src/main.rs": "fn main() {}\n",
				"crates/sdk/Cargo.toml":  "[package]\nname = \"sdk\"\nversion = \"0.1.0\"\n",
				"apps/b/Cargo.toml":      "[package]\nname = \"b\"\nversion = \"0.1.0\"\n\n[workspace]\n\n[dependencies]\nsdk = { path = \"../../crates/sdk\" }\n",
				"apps/b/src/main.rs":     "fn main() {}\n",
			},
			app: "apps/b",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.files[".git/HEAD"] = "ref: refs/heads/main\n"
			write(t, dir, tt.files)

			loc := locatedIn(t, dir, filepath.Join(dir, filepath.FromSlash(tt.app)), language.Rust)
			commands, err := loc.Commands()
			if err != nil {
				t.Fatalf("Commands() = %v", err)
			}

			if want := "./" + tt.app + "/bin/b"; loc.Root != dir || commands.Start != want || !strings.Contains(commands.Build, "--bin b ") {
				t.Errorf("Locate() = %+v, Commands() = %+v, want the context %s and the app's own binary b built and started — railpack would build the first member with a binary", loc, commands, dir)
			}
		})
	}
}

func TestARustAppBelowItsContextInstallsTheRustItsCrateRequires(t *testing.T) {
	for _, tt := range []struct {
		name     string
		manifest string
	}{
		{
			name:     "declared by the crate",
			manifest: "[package]\nname = \"web\"\nversion = \"0.1.0\"\nrust-version = \"1.92\"\n\n[workspace]\n\n",
		},
		{
			name:     "inherited from its workspace",
			manifest: "[workspace]\n\n[workspace.package]\nrust-version = \"1.92\"\n\n[package]\nname = \"web\"\nversion = \"0.1.0\"\nrust-version.workspace = true\n\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, map[string]string{
				".git/HEAD":       "ref: refs/heads/main\n",
				"sdk/Cargo.toml":  "[package]\nname = \"sdk\"\nversion = \"0.1.0\"\n",
				"web/Cargo.toml":  tt.manifest + "[dependencies]\nsdk = { path = \"../sdk\" }\n",
				"web/src/main.rs": "fn main() {}\n",
			})

			commands, err := locatedIn(t, dir, filepath.Join(dir, "web"), language.Rust).Commands()
			if err != nil {
				t.Fatalf("Commands() = %v", err)
			}
			if want := []workspace.Tool{{Name: "rust", Version: "1.92"}}; !reflect.DeepEqual(commands.Tools, want) {
				t.Errorf("Commands().Tools = %+v, want %+v — railpack reads a rust-version only from the Cargo.toml at the context root", commands.Tools, want)
			}
		})
	}
}
