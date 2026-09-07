package appbundler

import (
	"context"
	"debug/elf"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func goModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module fixture\n\ngo 1.24\n")
	write("main.go", "package main\n\nfunc main() {}\n")
	return dir
}

func compiled(t *testing.T, pkg, arch string) (string, string) {
	t.Helper()
	out := t.TempDir()
	appDir := filepath.Join(out, "apps", "web")
	funcDir := filepath.Join(appDir, "functions", "index.func")
	err := Compile(context.Background(), Compilation{
		App:     "web",
		Runtime: Runtime{Name: "go", Arch: arch},
		Package: pkg,
		FuncDir: funcDir,
		AppDir:  appDir,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return appDir, funcDir
}

func TestCompileWritesAnExecutableBootstrapForTheArchitectureItWasAsked(t *testing.T) {
	t.Parallel()

	for _, arch := range []struct {
		named   string
		machine elf.Machine
	}{
		{"x86_64", elf.EM_X86_64},
		{"arm64", elf.EM_AARCH64},
	} {
		t.Run(arch.named, func(t *testing.T) {
			t.Parallel()
			_, funcDir := compiled(t, goModule(t), arch.named)

			binary := filepath.Join(funcDir, BootstrapFile)
			info, err := os.Stat(binary)
			if err != nil {
				t.Fatalf("the compile wrote no %s: %v", BootstrapFile, err)
			}
			if info.Mode()&0o111 == 0 {
				t.Errorf("%s is mode %v, want the execute bit — the zip carries the mode and the function boots the binary", BootstrapFile, info.Mode())
			}

			read, err := elf.Open(binary)
			if err != nil {
				t.Fatalf("%s is no linux binary: %v", BootstrapFile, err)
			}
			defer read.Close()
			if read.Machine != arch.machine {
				t.Errorf("%s is built for %v, want %v — the function runs on the architecture the app named", BootstrapFile, read.Machine, arch.machine)
			}
		})
	}
}

func TestCompileNamesTheBootstrapAsTheHandlerTheFunctionBootsThrough(t *testing.T) {
	t.Parallel()
	appDir, funcDir := compiled(t, goModule(t), "x86_64")

	var config functionConfig
	readJSON(t, filepath.Join(funcDir, configFileName), &config)
	if config.Handler != BootstrapFile {
		t.Errorf("handler = %q, want %q", config.Handler, BootstrapFile)
	}
	if config.Runtime != (Runtime{Name: "go", Arch: "x86_64"}) {
		t.Errorf("runtime = %+v, want the go runtime at the architecture it was built for", config.Runtime)
	}
	if config.App != "web" {
		t.Errorf("app = %q, want %q", config.App, "web")
	}

	var descriptor edge.ServeDescriptor
	readJSON(t, filepath.Join(appDir, edge.ServeDescriptorFile), &descriptor)
	if descriptor.Runtime != "go" {
		t.Errorf("the serve descriptor names runtime %q, want %q", descriptor.Runtime, "go")
	}
	if descriptor.BuildID == "" {
		t.Error("the serve descriptor names no build id, and a release is identified by one")
	}
	if len(descriptor.Needs) != 0 {
		t.Errorf("the serve descriptor names needs %v: a go binary speaks http and asks the edge for nothing", descriptor.Needs)
	}
}

func TestCompileBuildsTheAppsOwnModuleWhateverWorkspaceEnclosesIt(t *testing.T) {
	t.Parallel()

	enclosing := t.TempDir()
	pkg := filepath.Join(enclosing, "server")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		filepath.Join(pkg, "go.mod"):        "module fixture\n\ngo 1.24\n",
		filepath.Join(pkg, "main.go"):       "package main\n\nfunc main() {}\n",
		filepath.Join(enclosing, "go.work"): "go 1.24\n\nuse ./elsewhere\n",
	} {
		if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, funcDir := compiled(t, pkg, "x86_64")
	if _, err := os.Stat(filepath.Join(funcDir, BootstrapFile)); err != nil {
		t.Fatalf("the compile wrote no %s: a go workspace above the app names modules a release never carries, and the app's own go.mod is what is built: %v", BootstrapFile, err)
	}
}

func TestCompileRefusesAnArchitectureGoBuildsNothingFor(t *testing.T) {
	t.Parallel()
	err := Compile(context.Background(), Compilation{
		App:     "web",
		Runtime: Runtime{Name: "go", Arch: "riscv"},
		Package: goModule(t),
		FuncDir: filepath.Join(t.TempDir(), "index.func"),
		AppDir:  t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "riscv") {
		t.Fatalf("err = %v, want a refusal naming riscv", err)
	}
}

func TestCompileReportsWhatTheCompilerSaidWhenTheAppDoesNotBuild(t *testing.T) {
	t.Parallel()
	pkg := goModule(t)
	if err := os.WriteFile(filepath.Join(pkg, "main.go"), []byte("package main\n\nfunc main() { undefinedCall() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Compile(context.Background(), Compilation{
		App:     "web",
		Runtime: Runtime{Name: "go", Arch: "x86_64"},
		Package: pkg,
		FuncDir: filepath.Join(t.TempDir(), "index.func"),
		AppDir:  t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "undefinedCall") {
		t.Fatalf("err = %v, want the compiler's own account of what did not build", err)
	}
}

func readJSON(t *testing.T, path string, into any) {
	t.Helper()
	read, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(read, into); err != nil {
		t.Fatalf("read %s as JSON: %v", path, err)
	}
}
