package provider

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func hostPlatformSuffix(t *testing.T) string {
	t.Helper()

	nodePlatform := map[string]string{
		"darwin":  "darwin",
		"linux":   "linux",
		"windows": "win32",
	}[runtime.GOOS]
	if nodePlatform == "" {
		t.Skipf("no node platform mapping for GOOS=%s", runtime.GOOS)
	}

	nodeArch := map[string]string{
		"amd64": "x64",
		"arm64": "arm64",
	}[runtime.GOARCH]
	if nodeArch == "" {
		t.Skipf("no node arch mapping for GOARCH=%s", runtime.GOARCH)
	}

	return nodePlatform + "-" + nodeArch
}

func requireNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found on PATH")
	}
}

func writeFakeProvider(t *testing.T, projectDir, pkg string) string {
	t.Helper()

	pkgDir := filepath.Join(projectDir, "node_modules", pkg)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", pkgDir, err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"`+pkg+`"}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	return pkgDir
}

func writeFakeBinary(t *testing.T, projectDir, pkg, binaryName string) string {
	t.Helper()

	binary := binaryName
	if runtime.GOOS == "windows" {
		binary = binaryName + ".exe"
	}

	binDir := filepath.Join(projectDir, "node_modules", pkg, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", binDir, err)
	}

	binPath := filepath.Join(binDir, binary)
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\necho fake\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return binPath
}

func TestLocate(t *testing.T) {
	t.Run("errors when node is not on PATH", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())

		_, err := Locate(context.Background(), t.TempDir(), "@ocel/provider-aws")
		if err == nil {
			t.Fatal("Locate() err = nil, want an error")
		}
		if !strings.Contains(err.Error(), "node") {
			t.Fatalf("error %q does not mention node", err.Error())
		}
	})

	t.Run("resolves the installed platform binary", func(t *testing.T) {
		t.Parallel()
		requireNode(t)
		suffix := hostPlatformSuffix(t)

		projectDir := t.TempDir()
		platformPkg := "@ocel/provider-aws-" + suffix
		want := writeFakeBinary(t, projectDir, platformPkg, "deploy")

		got, err := Locate(context.Background(), projectDir, "@ocel/provider-aws")
		if err != nil {
			t.Fatalf("Locate: %v", err)
		}
		if got != want {
			t.Fatalf("Locate() = %q, want %q", got, want)
		}
	})

	t.Run("resolves through a symlinked package", func(t *testing.T) {
		t.Parallel()
		requireNode(t)
		suffix := hostPlatformSuffix(t)

		store := t.TempDir()
		platformPkg := "@ocel/provider-aws-" + suffix
		want := writeFakeBinary(t, store, platformPkg, "deploy")

		projectDir := t.TempDir()
		writeFakeProvider(t, projectDir, "@ocel/provider-aws")
		scopeDir := filepath.Join(projectDir, "node_modules", "@ocel")
		if err := os.MkdirAll(scopeDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", scopeDir, err)
		}
		link := filepath.Join(scopeDir, "provider-aws-"+suffix)
		if err := os.Symlink(filepath.Join(store, "node_modules", platformPkg), link); err != nil {
			t.Fatalf("symlink: %v", err)
		}

		got, err := Locate(context.Background(), projectDir, "@ocel/provider-aws")
		if err != nil {
			t.Fatalf("Locate: %v", err)
		}
		if got != want {
			t.Fatalf("Locate() = %q, want %q", got, want)
		}
	})

	t.Run("resolves the platform package nested under a symlinked provider package", func(t *testing.T) {
		t.Parallel()
		requireNode(t)
		suffix := hostPlatformSuffix(t)

		workspace := t.TempDir()
		providerDir := writeFakeProvider(t, filepath.Join(workspace, "packages"), "provider-aws")
		platformPkg := "@ocel/provider-aws-" + suffix
		want := writeFakeBinary(t, providerDir, platformPkg, "deploy")

		projectDir := filepath.Join(workspace, "examples", "app")
		scopeDir := filepath.Join(projectDir, "node_modules", "@ocel")
		if err := os.MkdirAll(scopeDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", scopeDir, err)
		}
		if err := os.Symlink(providerDir, filepath.Join(scopeDir, "provider-aws")); err != nil {
			t.Fatalf("symlink: %v", err)
		}

		got, err := Locate(context.Background(), projectDir, "@ocel/provider-aws")
		if err != nil {
			t.Fatalf("Locate: %v", err)
		}
		if got != want {
			t.Fatalf("Locate() = %q, want %q", got, want)
		}
	})

	t.Run("errors when the platform package is not installed", func(t *testing.T) {
		t.Parallel()
		requireNode(t)
		suffix := hostPlatformSuffix(t)

		_, err := Locate(context.Background(), t.TempDir(), "@ocel/provider-aws")
		if err == nil {
			t.Fatal("Locate() err = nil, want an error")
		}
		if !strings.Contains(err.Error(), "@ocel/provider-aws-"+suffix) {
			t.Fatalf("error %q does not name the missing platform package", err.Error())
		}
	})

	t.Run("finds the real built cloud AWS binary", func(t *testing.T) {
		t.Parallel()
		requireNode(t)
		if _, err := exec.LookPath("go"); err != nil {
			t.Skip("go not found on PATH")
		}
		suffix := hostPlatformSuffix(t)

		repoRoot := repoRootDir(t)

		projectDir := t.TempDir()
		binDir := filepath.Join(projectDir, "node_modules", "@ocel/provider-aws-"+suffix, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", binDir, err)
		}
		binaryName := "deploy"
		if runtime.GOOS == "windows" {
			binaryName = "deploy.exe"
		}
		outPath := filepath.Join(binDir, binaryName)

		build := exec.Command("go", "build", "-o", outPath, "github.com/ocelhq/ocel/platform/aws/provider/cmd/deploy")
		build.Dir = repoRoot
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("go build the aws provider: %v\n%s", err, out)
		}

		got, err := Locate(context.Background(), projectDir, "@ocel/provider-aws")
		if err != nil {
			t.Fatalf("Locate: %v", err)
		}
		if got != outPath {
			t.Fatalf("Locate() = %q, want %q", got, outPath)
		}
		if info, err := os.Stat(got); err != nil || info.IsDir() {
			t.Fatalf("resolved path %q is not a file", got)
		}
	})
}

func TestLocateReturnsPromptlyOnCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fake node")
	}

	fakePathDir := t.TempDir()
	fakeNode := filepath.Join(fakePathDir, "node")
	if err := os.WriteFile(fakeNode, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatalf("write fake node: %v", err)
	}
	t.Setenv("PATH", fakePathDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := Locate(ctx, t.TempDir(), "@ocel/provider-aws")
		done <- err
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Locate() err = nil, want an error from the cancelled context")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Locate did not return promptly after the context was cancelled")
	}
}

func repoRootDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.work")); err != nil {
		t.Fatalf("computed repo root %q does not contain go.work: %v", dir, err)
	}
	return dir
}
