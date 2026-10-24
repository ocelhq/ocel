package toolchain

import (
	"context"
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
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

func compileRust(t *testing.T, source, architecture string) (string, string, error) {
	t.Helper()
	out := t.TempDir()
	appDir := filepath.Join(out, "apps", "web")
	functionDir := filepath.Join(appDir, "functions", "index.func")
	err := Compile(context.Background(), Compilation{
		App:         "web",
		Framework:   buildoutput.Framework{Name: buildoutput.FrameworkRust, Arch: architecture},
		Source:      source,
		FunctionDir: functionDir,
		AppDir:      appDir,
	})
	return appDir, functionDir, err
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

			_, functionDir, err := compileRust(t, rustCrate(t), platform.named)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}

			binary := filepath.Join(functionDir, "web")
			info, err := os.Stat(binary)
			if err != nil {
				t.Fatalf("the compile wrote no binary named after the app: %v", err)
			}
			if info.Mode()&0o111 == 0 {
				t.Errorf("the binary is mode %v, want the execute bit — the zip records the mode and the runtime execs it", info.Mode())
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
					t.Errorf("the binary asks for a dynamic loader, and a function's host has no libc the app was linked against")
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

			_, functionDir, err := compileRust(t, cCrate(t), platform.named)
			if err != nil {
				t.Fatalf("compile: %v — a crate that compiles C is built with the toolchain the docs name, and ocel links what it produces", err)
			}

			binary := filepath.Join(functionDir, "web")
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
					t.Errorf("the binary asks for a dynamic loader: the C it links is static musl, and a function's host has no libc")
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

	appDir, functionDir, err := compileRust(t, rustCrate(t), arch.X8664)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	var config buildoutput.FunctionConfig
	readJSON(t, filepath.Join(functionDir, buildoutput.FunctionConfigFile), &config)
	if config.EntryFile != "web" {
		t.Errorf("entryFile = %q, want the binary named after the app", config.EntryFile)
	}
	if len(config.Command) != 1 || config.Command[0] != "./web" {
		t.Errorf("command = %q, want the artifact's own binary, which whatever hosts it execs", config.Command)
	}
	if config.Framework != (buildoutput.Framework{Name: "rust", Arch: "x86_64"}) {
		t.Errorf("runtime = %+v, want the rust runtime at the architecture it was built for", config.Framework)
	}

	var hosting buildoutput.Hosting
	readJSON(t, filepath.Join(appDir, buildoutput.HostingFile), &hosting)
	if hosting.Framework != "rust" {
		t.Errorf("hosting.json names runtime %q, want %q", hosting.Framework, "rust")
	}
	if hosting.FrameworkBuildID == "" {
		t.Error("hosting.json names no build id, and a release is identified by one")
	}
	if len(hosting.Needs) != 0 {
		t.Errorf("hosting.json names needs %v: a rust binary speaks http and asks the edge for nothing", hosting.Needs)
	}
}

func TestCompileBuildsTheAppsCrateInsideTheCargoWorkspaceThatContainsIt(t *testing.T) {
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

	_, functionDir, err := compileRust(t, filepath.Join(workspace, "apps", "web"), arch.X8664)
	if err != nil {
		t.Fatalf("compile: %v — only the app's own crate is built, and a sibling that does not compile is no concern of it", err)
	}
	if _, err := os.Stat(filepath.Join(functionDir, "web")); err != nil {
		t.Fatalf("the compile wrote no binary named after the app from the workspace's shared target directory: %v", err)
	}
}

func TestCompileRefusesARustAppDirectoryWithNoCrate(t *testing.T) {
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
			"Cargo.toml":    twoBinCrateManifest,
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
			if name == "two binaries" && (!strings.Contains(err.Error(), `"entrypoint"`) || !strings.Contains(err.Error(), `"worker"`)) {
				t.Errorf("err = %v, want it to offer the binaries to set \"entrypoint\" to", err)
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

const twoBinCrateManifest = rustCrateManifest + "\n[[bin]]\nname = \"server\"\npath = \"src/main.rs\"\n\n[[bin]]\nname = \"worker\"\npath = \"src/worker.rs\"\n"

func compileRustBin(t *testing.T, source, entrypoint string) (string, error) {
	t.Helper()
	appDir := filepath.Join(t.TempDir(), "apps", "web")
	functionDir := filepath.Join(appDir, "functions", "index.func")
	err := Compile(context.Background(), Compilation{
		App:         "web",
		Framework:   buildoutput.Framework{Name: buildoutput.FrameworkRust, Arch: arch.X8664},
		Source:      source,
		Entrypoint:  entrypoint,
		FunctionDir: functionDir,
		AppDir:      appDir,
	})
	return functionDir, err
}

func TestCompileBuildsOnlyTheBinaryTheEntrypointNames(t *testing.T) {
	t.Parallel()
	needsRustTarget(t, arch.X8664)

	source := t.TempDir()
	writeTree(t, source, map[string]string{
		"Cargo.toml":    twoBinCrateManifest,
		"src/main.rs":   "fn main() { undefined_call() }\n",
		"src/worker.rs": "fn main() {}\n",
	})

	functionDir, err := compileRustBin(t, source, "worker")
	if err != nil {
		t.Fatalf("compile: %v — the entrypoint names worker, and the server binary that does not compile is never built", err)
	}
	if _, err := os.Stat(filepath.Join(functionDir, "web")); err != nil {
		t.Fatalf("the compile wrote no binary named after the app: %v", err)
	}
}

func TestCompileBuildsTheBinaryTheCrateRunsByDefaultWhenNoEntrypointNamesOne(t *testing.T) {
	t.Parallel()
	needsRustTarget(t, arch.X8664)

	source := t.TempDir()
	writeTree(t, source, map[string]string{
		"Cargo.toml":    strings.Replace(twoBinCrateManifest, "[package]\n", "[package]\ndefault-run = \"worker\"\n", 1),
		"src/main.rs":   "fn main() { undefined_call() }\n",
		"src/worker.rs": "fn main() {}\n",
	})

	functionDir, err := compileRustBin(t, source, "")
	if err != nil {
		t.Fatalf("compile: %v — default-run names worker, and the server binary that does not compile is never built", err)
	}
	if _, err := os.Stat(filepath.Join(functionDir, "web")); err != nil {
		t.Fatalf("the compile wrote no binary named after the app: %v", err)
	}
}

func TestCompileRefusesAnEntrypointNamingNoBinaryOfTheCrate(t *testing.T) {
	t.Parallel()
	needsRustTarget(t, arch.X8664)

	source := t.TempDir()
	writeTree(t, source, map[string]string{
		"Cargo.toml":    twoBinCrateManifest,
		"src/main.rs":   "fn main() {}\n",
		"src/worker.rs": "fn main() {}\n",
	})

	_, err := compileRustBin(t, source, "api")
	if err == nil {
		t.Fatal("compile built a binary the crate does not have")
	}
	for _, want := range []string{`"web"`, `"api"`, `"server"`, `"worker"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %s — the app, the entrypoint and the binaries the crate does build", err, want)
		}
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

	_, functionDir, err := compileRust(t, source, arch.X8664)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(functionDir, "web")); err != nil {
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

	_, functionDir, err := compileRust(t, link, arch.X8664)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(functionDir, "web")); err != nil {
		t.Fatalf("the compile wrote no binary: %v", err)
	}
}

const dependsOnSys = "[dependencies]\nsys = { path = \"sys\" }\n\n[workspace]\n"

func cBuildingWorkspace(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"Cargo.toml":     "[package]\nname = \"server\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n" + manifest,
		"src/main.rs":    "fn main() {}\n",
		"sys/Cargo.toml": "[package]\nname = \"sys\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[build-dependencies]\ncc = { path = \"../cc\" }\n",
		"sys/build.rs":   "fn main() {}\n",
		"sys/src/lib.rs": "",
		"cc/Cargo.toml":  "[package]\nname = \"cc\"\nversion = \"1.0.0\"\nedition = \"2021\"\n",
		"cc/src/lib.rs":  "",
	})
	t.Setenv("CARGO_TARGET_DIR", filepath.Join(dir, "target"))
	return dir
}

func cargoBuilt(workspace string) bool {
	_, err := os.Stat(filepath.Join(workspace, "target"))
	return err == nil
}

func withNoCCompilerNamed(t *testing.T) {
	t.Helper()
	var path []string
	for _, tool := range []string{"cargo", "rustc"} {
		found, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s is not on PATH", tool)
		}
		path = append(path, filepath.Dir(found))
	}
	for _, dir := range path {
		for _, compiler := range []string{"aarch64-linux-musl-gcc", "x86_64-linux-musl-gcc", "musl-gcc"} {
			if _, err := os.Stat(filepath.Join(dir, compiler)); err == nil {
				t.Skipf("%s sits beside cargo in %s", compiler, dir)
			}
		}
	}
	host, err := exec.Command("rustc", "-vV").Output()
	if err != nil {
		t.Skipf("rustc -vV: %v", err)
	}
	unset := []string{"TARGET_CC", "CC", "CROSS_COMPILE"}
	for _, architecture := range []string{arch.X8664, arch.ARM64} {
		target, _ := arch.RustTarget(architecture)
		if strings.Contains(string(host), "host: "+target+"\n") {
			t.Skipf("rustc's host is %s, and cc-rs compiles C for its own host with the host's cc", target)
		}
		underscored := strings.ReplaceAll(target, "-", "_")
		unset = append(unset, "CC_"+target, "CC_"+underscored, "CARGO_TARGET_"+strings.ToUpper(underscored)+"_LINKER")
	}
	for _, name := range unset {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	t.Setenv("PATH", strings.Join(path, string(os.PathListSeparator)))
}

func compileRustWithEnv(t *testing.T, source, architecture string, env map[string]string) error {
	t.Helper()
	appDir := filepath.Join(t.TempDir(), "apps", "web")
	return Compile(context.Background(), Compilation{
		App:         "web",
		Framework:   buildoutput.Framework{Name: buildoutput.FrameworkRust, Arch: architecture},
		Source:      source,
		FunctionDir: filepath.Join(appDir, "functions", "index.func"),
		AppDir:      appDir,
		Env:         env,
	})
}

func TestCompileRefusesACrateThatCompilesCForTheTargetWhenNoCCompilerForItIsNamed(t *testing.T) {
	withNoCCompilerNamed(t)
	source := cBuildingWorkspace(t, dependsOnSys)

	err := compileRustWithEnv(t, source, arch.ARM64, nil)
	if err == nil {
		t.Fatal("compile handed cargo a crate whose build script looks for a C compiler for the target that is nowhere")
	}
	for _, want := range []string{`"web"`, "sys", "aarch64-unknown-linux-musl", "aarch64-linux-musl-gcc", "CC_aarch64_unknown_linux_musl"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %s — the app, the crate that compiles C, ocel's target and what to install or set", err, want)
		}
	}
	if cargoBuilt(source) {
		t.Error("cargo build ran before the refusal")
	}
}

func TestCompileHandsCargoACrateThatCompilesCOnceACCompilerForTheTargetIsNamed(t *testing.T) {
	for name, named := range map[string]struct {
		architecture string
		osEnv        map[string]string
		buildEnv     map[string]string
		files        map[string]string
		onPath       string
	}{
		"CC with the target":             {architecture: arch.ARM64, osEnv: map[string]string{"CC_aarch64-unknown-linux-musl": "ocel-named-cc"}},
		"CC with the underscored target": {architecture: arch.ARM64, osEnv: map[string]string{"CC_aarch64_unknown_linux_musl": "ocel-named-cc"}},
		"TARGET_CC":                      {architecture: arch.ARM64, osEnv: map[string]string{"TARGET_CC": "ocel-named-cc"}},
		"CC":                             {architecture: arch.ARM64, osEnv: map[string]string{"CC": "ocel-named-cc"}},
		"CROSS_COMPILE":                  {architecture: arch.ARM64, osEnv: map[string]string{"CROSS_COMPILE": "ocel-named-"}},
		"the build's own env":            {architecture: arch.ARM64, buildEnv: map[string]string{"CC_aarch64_unknown_linux_musl": "ocel-named-cc"}},
		"a cargo config's env":           {architecture: arch.ARM64, files: map[string]string{".cargo/config.toml": "[env]\nCC_aarch64_unknown_linux_musl = \"ocel-named-cc\"\n"}},
		"a gcc the linker names":         {architecture: arch.ARM64, files: map[string]string{".cargo/config.toml": "[target.aarch64-unknown-linux-musl]\nlinker = \"/opt/musl/bin/aarch64-linux-musl-gcc\"\n"}},
		"aarch64-linux-musl-gcc on PATH": {architecture: arch.ARM64, onPath: "aarch64-linux-musl-gcc"},
		"x86_64-linux-musl-gcc on PATH":  {architecture: arch.X8664, onPath: "x86_64-linux-musl-gcc"},
		"musl-gcc on PATH":               {architecture: arch.X8664, onPath: "musl-gcc"},
	} {
		t.Run(name, func(t *testing.T) {
			withNoCCompilerNamed(t)
			source := cBuildingWorkspace(t, dependsOnSys)
			writeTree(t, source, named.files)
			for variable, value := range named.osEnv {
				t.Setenv(variable, value)
			}
			if named.onPath != "" {
				bin := t.TempDir()
				writeTree(t, bin, map[string]string{named.onPath: "#!/bin/sh\nexit 1\n"})
				if err := os.Chmod(filepath.Join(bin, named.onPath), 0o755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			}

			err := compileRustWithEnv(t, source, named.architecture, named.buildEnv)
			if !cargoBuilt(source) {
				t.Fatalf("compile refused before cargo build ran (%v), and cc-rs finds the compiler that is named", err)
			}
		})
	}
}

func TestCompileHandsCargoACrateWhoseCIsCompiledOnlyForTheHost(t *testing.T) {
	for name, sys := range map[string]struct {
		dependency string
		files      map[string]string
	}{
		"a build dependency's": {dependency: "[build-dependencies]\nsys = { path = \"sys\" }\n"},
		"a dev dependency's":   {dependency: "[dev-dependencies]\nsys = { path = \"sys\" }\n"},
		"a proc macro's": {
			dependency: "[dependencies]\nderive = { path = \"derive\" }\n",
			files: map[string]string{
				"derive/Cargo.toml": "[package]\nname = \"derive\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[lib]\nproc-macro = true\n\n[dependencies]\nsys = { path = \"../sys\" }\n",
				"derive/src/lib.rs": "",
			},
		},
		"another platform's": {dependency: "[target.'cfg(target_os = \"haiku\")'.dependencies]\nsys = { path = \"sys\" }\n"},
		"no crate's":         {dependency: ""},
	} {
		t.Run(name, func(t *testing.T) {
			withNoCCompilerNamed(t)
			source := cBuildingWorkspace(t, sys.dependency+"\n[workspace]\n")
			writeTree(t, source, sys.files)

			err := compileRustWithEnv(t, source, arch.ARM64, nil)
			if !cargoBuilt(source) {
				t.Fatalf("compile refused before cargo build ran (%v), and no C is compiled for the target", err)
			}
		})
	}
}

func TestCompileHandsCargoACrateWhoseCIsBehindAFeatureItsOwnBuildLeavesOff(t *testing.T) {
	shared := map[string]string{
		"shared/Cargo.toml": "[package]\nname = \"shared\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dependencies]\nsys = { path = \"../sys\", optional = true }\n\n[features]\nc = [\"dep:sys\"]\n",
		"shared/src/lib.rs": "",
		"sys/build.rs":      "fn main() { panic!(\"sys is built\") }\n",
	}
	for name, enabled := range map[string]struct {
		manifest string
		files    map[string]string
	}{
		"by a sibling workspace member": {
			manifest: "[dependencies]\nshared = { path = \"shared\" }\n\n[workspace]\nmembers = [\"tool\"]\n",
			files: map[string]string{
				"tool/Cargo.toml":  "[package]\nname = \"tool\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dependencies]\nshared = { path = \"../shared\", features = [\"c\"] }\n",
				"tool/src/main.rs": "fn main() {}\n",
			},
		},
		"by the app's dev dependency": {
			manifest: "[dependencies]\nshared = { path = \"shared\" }\n\n[dev-dependencies]\nshared = { path = \"shared\", features = [\"c\"] }\n\n[workspace]\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			withNoCCompilerNamed(t)
			source := cBuildingWorkspace(t, enabled.manifest)
			writeTree(t, source, shared)
			writeTree(t, source, enabled.files)

			err := compileRustWithEnv(t, source, arch.ARM64, nil)
			if !cargoBuilt(source) {
				t.Fatalf("compile refused before cargo build ran (%v), and the app's own build leaves off the feature that compiles C", err)
			}
			if err != nil && strings.Contains(err.Error(), "sys is built") {
				t.Errorf("cargo built sys, which the app's own build leaves out: %v", err)
			}
		})
	}
}
