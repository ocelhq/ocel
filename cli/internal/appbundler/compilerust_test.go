package appbundler

import (
	"context"
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/arch"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const rustCrateManifest = "[package]\nname = \"server\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[workspace]\n"

func needsRustTarget(t *testing.T, architecture string) {
	t.Helper()
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo is not on PATH")
	}
	target, _ := arch.RustTarget(architecture)
	libdir, err := exec.Command("rustc", "--print", "target-libdir", "--target", target).Output()
	if err != nil {
		t.Skipf("rustc cannot name a library directory for %s: %v", target, err)
	}
	if _, err := os.Stat(strings.TrimSpace(string(libdir))); err != nil {
		t.Skipf("the %s standard library is not installed: rustup target add %s", target, target)
	}
}

func rustCrate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"Cargo.toml":  rustCrateManifest,
		"src/main.rs": "fn main() {}\n",
	})
	return dir
}

func compileRust(t *testing.T, source, arch string) (string, string, error) {
	t.Helper()
	out := t.TempDir()
	appDir := filepath.Join(out, "apps", "web")
	funcDir := filepath.Join(appDir, "functions", "index.func")
	err := Compile(context.Background(), Compilation{
		App:       "web",
		Framework: providerkit.Framework{Name: providerkit.FrameworkRust, Arch: arch},
		Source:    source,
		FuncDir:   funcDir,
		AppDir:    appDir,
	})
	return appDir, funcDir, err
}

func TestCompileWritesAStaticRustBinaryForTheArchitectureItWasAsked(t *testing.T) {
	t.Parallel()

	for _, platform := range []struct {
		named   string
		machine elf.Machine
	}{
		{arch.X8664, elf.EM_X86_64},
		{arch.ARM64, elf.EM_AARCH64},
	} {
		t.Run(platform.named, func(t *testing.T) {
			t.Parallel()
			needsRustTarget(t, platform.named)

			_, funcDir, err := compileRust(t, rustCrate(t), platform.named)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}

			binary := filepath.Join(funcDir, "web")
			info, err := os.Stat(binary)
			if err != nil {
				t.Fatalf("the compile wrote no binary named after the app: %v", err)
			}
			if info.Mode()&0o111 == 0 {
				t.Errorf("the binary is mode %v, want the execute bit — the zip carries the mode and the runtime execs it", info.Mode())
			}

			read, err := elf.Open(binary)
			if err != nil {
				t.Fatalf("the binary is no linux executable: %v", err)
			}
			defer read.Close()
			if read.Machine != platform.machine {
				t.Errorf("the binary is built for %v, want %v — the function runs on the architecture the app named", read.Machine, platform.machine)
			}
			for _, program := range read.Progs {
				if program.Type == elf.PT_INTERP {
					t.Errorf("the binary asks for a dynamic loader, and a function's host carries no libc the app was linked against")
				}
			}
		})
	}
}

const cCrateBuildScript = `use std::env;
use std::process::Command;

fn main() {
    let target = env::var("TARGET").unwrap();
    let out = env::var("OUT_DIR").unwrap();
    let cc = env::var(format!("CC_{}", target.replace('-', "_"))).unwrap();
    let mut said = cc.split_whitespace();
    let mut compile = Command::new(said.next().unwrap());
    compile.args(said);
    assert!(compile
        .args(["-c", "answer.c", "-o", &format!("{out}/answer.o")])
        .status()
        .unwrap()
        .success());
    assert!(Command::new("ar")
        .args(["rcs", &format!("{out}/libanswer.a"), &format!("{out}/answer.o")])
        .status()
        .unwrap()
        .success());
    println!("cargo:rustc-link-search=native={out}");
    println!("cargo:rustc-link-lib=static=answer");
    println!("cargo:rerun-if-changed=answer.c");
}
`

const cCrateAnswer = "42"

func muslCompilerFor(t *testing.T, target string) string {
	t.Helper()
	if named := strings.Replace(target, "-unknown-", "-", 1) + "-gcc"; lookedUp(named) {
		return named
	}
	if lookedUp("zig") {
		return "zig cc -target " + strings.Replace(target, "-unknown-", "-", 1)
	}
	t.Skipf("no musl C compiler for %s: put a %s-gcc or zig on PATH", target, strings.Replace(target, "-unknown-", "-", 1))
	return ""
}

func lookedUp(named string) bool {
	_, err := exec.LookPath(named)
	return err == nil
}

func cCrate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"Cargo.toml":  rustCrateManifest,
		"build.rs":    cCrateBuildScript,
		"answer.c":    "int ocel_answer(void) { return " + cCrateAnswer + "; }\n",
		"src/main.rs": "extern \"C\" {\n    fn ocel_answer() -> i32;\n}\n\nfn main() {\n    print!(\"{}\", unsafe { ocel_answer() });\n}\n",
	})
	return dir
}

func TestCompileLinksTheCACrateBuildsForTheTargetIntoTheStaticBinary(t *testing.T) {
	for _, platform := range []struct {
		named   string
		machine elf.Machine
		goarch  string
	}{
		{arch.X8664, elf.EM_X86_64, "amd64"},
		{arch.ARM64, elf.EM_AARCH64, "arm64"},
	} {
		t.Run(platform.named, func(t *testing.T) {
			needsRustTarget(t, platform.named)
			target, _ := arch.RustTarget(platform.named)
			t.Setenv("CC_"+strings.ReplaceAll(target, "-", "_"), muslCompilerFor(t, target))

			_, funcDir, err := compileRust(t, cCrate(t), platform.named)
			if err != nil {
				t.Fatalf("compile: %v — a crate that compiles C is built with the toolchain the docs name, and ocel links what it produces", err)
			}

			binary := filepath.Join(funcDir, "web")
			read, err := elf.Open(binary)
			if err != nil {
				t.Fatalf("the binary is no linux executable: %v", err)
			}
			defer read.Close()
			if read.Machine != platform.machine {
				t.Errorf("the binary is built for %v, want %v", read.Machine, platform.machine)
			}
			for _, program := range read.Progs {
				if program.Type == elf.PT_INTERP {
					t.Errorf("the binary asks for a dynamic loader: the C it links is static musl, and a function's host carries no libc")
				}
			}
			if runtime.GOARCH != platform.goarch {
				return
			}
			said, err := exec.Command(binary).Output()
			if err != nil {
				t.Fatalf("run the binary: %v", err)
			}
			if string(said) != cCrateAnswer {
				t.Errorf("the binary said %q, want %q — the C the crate compiled runs in the artifact that is deployed", said, cCrateAnswer)
			}
		})
	}
}

func TestCompileDeclaresTheCommandARustArtifactIsServedBy(t *testing.T) {
	t.Parallel()
	needsRustTarget(t, arch.X8664)

	appDir, funcDir, err := compileRust(t, rustCrate(t), arch.X8664)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	var config providerkit.FunctionConfig
	readJSON(t, filepath.Join(funcDir, providerkit.FunctionConfigFile), &config)
	if config.Handler != "web" {
		t.Errorf("handler = %q, want the binary named after the app", config.Handler)
	}
	if len(config.Command) != 1 || config.Command[0] != "./web" {
		t.Errorf("command = %q, want the artifact's own binary, which whatever hosts it execs", config.Command)
	}
	if config.Framework != (providerkit.Framework{Name: "rust", Arch: "x86_64"}) {
		t.Errorf("runtime = %+v, want the rust runtime at the architecture it was built for", config.Framework)
	}

	var descriptor edge.ServeDescriptor
	readJSON(t, filepath.Join(appDir, edge.ServeDescriptorFile), &descriptor)
	if descriptor.Framework != "rust" {
		t.Errorf("the serve descriptor names runtime %q, want %q", descriptor.Framework, "rust")
	}
	if descriptor.BuildID == "" {
		t.Error("the serve descriptor names no build id, and a release is identified by one")
	}
	if len(descriptor.Needs) != 0 {
		t.Errorf("the serve descriptor names needs %v: a rust binary speaks http and asks the edge for nothing", descriptor.Needs)
	}
}

func TestCompileBuildsTheAppsCrateInsideTheCargoWorkspaceThatHoldsIt(t *testing.T) {
	t.Parallel()
	needsRustTarget(t, arch.X8664)

	workspace := t.TempDir()
	writeTree(t, workspace, map[string]string{
		"Cargo.toml":              "[workspace]\nmembers = [\"apps/web\", \"apps/worker\"]\nresolver = \"2\"\n",
		"apps/web/Cargo.toml":     "[package]\nname = \"web-server\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
		"apps/web/src/main.rs":    "fn main() {}\n",
		"apps/worker/Cargo.toml":  "[package]\nname = \"worker\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
		"apps/worker/src/main.rs": "fn main() { undefined_call() }\n",
	})

	_, funcDir, err := compileRust(t, filepath.Join(workspace, "apps", "web"), arch.X8664)
	if err != nil {
		t.Fatalf("compile: %v — only the app's own crate is built, and a sibling that does not compile is no concern of it", err)
	}
	if _, err := os.Stat(filepath.Join(funcDir, "web")); err != nil {
		t.Fatalf("the compile wrote no binary named after the app from the workspace's shared target directory: %v", err)
	}
}

func TestCompileRefusesARustAppDirectoryHoldingNoCrate(t *testing.T) {
	t.Parallel()
	needsRustTarget(t, arch.X8664)

	source := t.TempDir()
	writeTree(t, source, map[string]string{"src/main.rs": "fn main() {}\n"})

	_, _, err := compileRust(t, source, arch.X8664)
	if err == nil || !strings.Contains(err.Error(), source) || !strings.Contains(err.Error(), "Cargo.toml") {
		t.Fatalf("err = %v, want a refusal naming %s and Cargo.toml", err, source)
	}
}

func TestCompileRefusesACrateThatBuildsNoOneBinary(t *testing.T) {
	t.Parallel()
	needsRustTarget(t, arch.X8664)

	for name, files := range map[string]map[string]string{
		"no binary": {
			"Cargo.toml": rustCrateManifest,
			"src/lib.rs": "",
		},
		"two binaries": {
			"Cargo.toml":    rustCrateManifest + "\n[[bin]]\nname = \"server\"\npath = \"src/main.rs\"\n\n[[bin]]\nname = \"worker\"\npath = \"src/worker.rs\"\n",
			"src/main.rs":   "fn main() {}\n",
			"src/worker.rs": "fn main() {}\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			source := t.TempDir()
			writeTree(t, source, files)

			_, _, err := compileRust(t, source, arch.X8664)
			if err == nil || !strings.Contains(err.Error(), `"web"`) || !strings.Contains(err.Error(), "server") {
				t.Fatalf("err = %v, want a refusal naming the app and the crate, which must build exactly one binary to serve", err)
			}
		})
	}
}

func TestCompileRefusesAnArchitectureRustBuildsNothingFor(t *testing.T) {
	t.Parallel()

	_, _, err := compileRust(t, rustCrate(t), "riscv")
	if err == nil || !strings.Contains(err.Error(), "riscv") {
		t.Fatalf("err = %v, want a refusal naming riscv", err)
	}
}

func TestCompileRefusesAnEntrypointForARustApp(t *testing.T) {
	t.Parallel()

	source := rustCrate(t)
	if err := os.MkdirAll(filepath.Join(source, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := Compile(context.Background(), Compilation{
		App:        "web",
		Framework:  providerkit.Framework{Name: providerkit.FrameworkRust},
		Source:     source,
		Entrypoint: "bin",
		FuncDir:    filepath.Join(t.TempDir(), "index.func"),
		AppDir:     t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "bin") || !strings.Contains(err.Error(), "Cargo.toml") {
		t.Fatalf("err = %v, want a refusal naming the entrypoint and the crate a rust app is built from", err)
	}
}

func TestCompileReportsWhatCargoSaidWhenTheCrateDoesNotBuild(t *testing.T) {
	t.Parallel()
	needsRustTarget(t, arch.X8664)

	source := rustCrate(t)
	writeTree(t, source, map[string]string{"src/main.rs": "fn main() { undefined_call() }\n"})

	_, _, err := compileRust(t, source, arch.X8664)
	if err == nil || !strings.Contains(err.Error(), "undefined_call") {
		t.Fatalf("err = %v, want cargo's own account of what did not build", err)
	}
}

func TestCompileLinksWithTheLinkerACargoConfigNamesForTheTarget(t *testing.T) {
	t.Parallel()
	needsRustTarget(t, arch.X8664)

	for name, config := range map[string]string{
		".cargo/config.toml":    "[target.x86_64-unknown-linux-musl]\nlinker = \"ocel-configured-linker\"\n",
		".cargo/config":         "[target.x86_64-unknown-linux-musl]\nlinker = \"ocel-configured-linker\"\n",
		"../.cargo/config.toml": "[target.x86_64-unknown-linux-musl]\nlinker = \"ocel-configured-linker\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			source := filepath.Join(t.TempDir(), "web")
			writeTree(t, source, map[string]string{
				"Cargo.toml":  rustCrateManifest,
				"src/main.rs": "fn main() {}\n",
			})
			writeTree(t, source, map[string]string{name: config})

			_, _, err := compileRust(t, source, arch.X8664)
			if err == nil || !strings.Contains(err.Error(), "ocel-configured-linker") {
				t.Fatalf("err = %v, want cargo to have reached for the linker the config names rather than one ocel chose over it", err)
			}
		})
	}
}

func TestCompileLinksWithTheLinkerCargoHomeNamesForTheTarget(t *testing.T) {
	needsRustTarget(t, arch.X8664)

	home := t.TempDir()
	writeTree(t, home, map[string]string{"config.toml": "[target.x86_64-unknown-linux-musl]\nlinker = \"ocel-configured-linker\"\n"})
	t.Setenv("CARGO_HOME", home)

	_, _, err := compileRust(t, rustCrate(t), arch.X8664)
	if err == nil || !strings.Contains(err.Error(), "ocel-configured-linker") {
		t.Fatalf("err = %v, want cargo to have reached for the linker CARGO_HOME's config names", err)
	}
}

func TestCompileLinksARustBinaryWhenTheCargoConfigNamesALinkerOnlyForAnotherTarget(t *testing.T) {
	t.Parallel()
	needsRustTarget(t, arch.X8664)

	source := rustCrate(t)
	writeTree(t, source, map[string]string{".cargo/config.toml": "[target.aarch64-unknown-linux-musl]\nlinker = \"ocel-configured-linker\"\n"})

	_, funcDir, err := compileRust(t, source, arch.X8664)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(funcDir, "web")); err != nil {
		t.Fatalf("the compile wrote no binary: %v", err)
	}
}

func TestCompileBuildsACrateReachedThroughASymlink(t *testing.T) {
	t.Parallel()
	needsRustTarget(t, arch.X8664)

	link := filepath.Join(t.TempDir(), "web")
	if err := os.Symlink(rustCrate(t), link); err != nil {
		t.Fatal(err)
	}

	_, funcDir, err := compileRust(t, link, arch.X8664)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(funcDir, "web")); err != nil {
		t.Fatalf("the compile wrote no binary: %v", err)
	}
}
