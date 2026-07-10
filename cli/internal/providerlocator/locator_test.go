package providerlocator

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// hostPlatformSuffix mirrors packages/ocel/bin/run.js's (and
// resolve-provider.cjs's) platform/arch naming, translated from Go's
// GOOS/GOARCH so tests can build the same node_modules layout Node itself
// would expect on this host.
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

// writeFakeBinary creates projectDir/node_modules/<pkg>/bin/<binaryName>
// (plus its .exe sibling on Windows), returning its path.
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

func TestLocate_ResolvesInstalledPlatformBinary(t *testing.T) {
	requireNode(t)
	suffix := hostPlatformSuffix(t)

	projectDir := t.TempDir()
	platformPkg := "@ocel/provider-aws-" + suffix
	want := writeFakeBinary(t, projectDir, platformPkg, "ocelaws")

	got, err := Locate(projectDir, "@ocel/provider-aws")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got != want {
		t.Fatalf("Locate() = %q, want %q", got, want)
	}
}

func TestLocate_ResolvesThroughSymlinkedPackage(t *testing.T) {
	requireNode(t)
	suffix := hostPlatformSuffix(t)

	// pnpm installs direct dependencies into node_modules as a symlink into
	// its own content-addressed store, rather than a real directory. Locate
	// must resolve through that exactly as it would a flat npm layout.
	store := t.TempDir()
	platformPkg := "@ocel/provider-aws-" + suffix
	want := writeFakeBinary(t, store, platformPkg, "ocelaws")

	projectDir := t.TempDir()
	scopeDir := filepath.Join(projectDir, "node_modules", "@ocel")
	if err := os.MkdirAll(scopeDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", scopeDir, err)
	}
	link := filepath.Join(scopeDir, "provider-aws-"+suffix)
	if err := os.Symlink(filepath.Join(store, "node_modules", platformPkg), link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := Locate(projectDir, "@ocel/provider-aws")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got != want {
		t.Fatalf("Locate() = %q, want %q", got, want)
	}
}

func TestLocate_ErrorWhenPackageNotInstalled(t *testing.T) {
	requireNode(t)
	suffix := hostPlatformSuffix(t)

	projectDir := t.TempDir()

	_, err := Locate(projectDir, "@ocel/provider-aws")
	if err == nil {
		t.Fatal("Locate() err = nil, want an error")
	}
	if !strings.Contains(err.Error(), "@ocel/provider-aws-"+suffix) {
		t.Fatalf("error %q does not name the missing platform package", err.Error())
	}
}

func TestLocate_ErrorWhenNodeNotOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := Locate(t.TempDir(), "@ocel/provider-aws")
	if err == nil {
		t.Fatal("Locate() err = nil, want an error")
	}
	if !strings.Contains(err.Error(), "node") {
		t.Fatalf("error %q does not mention node", err.Error())
	}
}

// TestLocate_FindsRealBuiltCloudAWSBinary is the standalone packaging smoke
// test: it builds the actual cloud/aws provider binary (T7) and confirms
// Locate finds it via the same @ocel/provider-aws-<platform>-<arch>
// convention the real npm packages use — independent of the CLI's spawn
// wiring (ocelhq-x53.10) or the full e2e run (ocelhq-x53.11), so a
// packaging bug surfaces here.
func TestLocate_FindsRealBuiltCloudAWSBinary(t *testing.T) {
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
	binaryName := "ocelaws"
	if runtime.GOOS == "windows" {
		binaryName = "ocelaws.exe"
	}
	outPath := filepath.Join(binDir, binaryName)

	build := exec.Command("go", "build", "-o", outPath, "github.com/ocelhq/ocel/cloud/aws/cmd/ocelaws")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build cloud/aws: %v\n%s", err, out)
	}

	got, err := Locate(projectDir, "@ocel/provider-aws")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got != outPath {
		t.Fatalf("Locate() = %q, want %q", got, outPath)
	}
	if info, err := os.Stat(got); err != nil || info.IsDir() {
		t.Fatalf("resolved path %q is not a file", got)
	}
}

// repoRootDir returns the monorepo root (the directory containing go.work),
// derived from this file's own location rather than the test's working
// directory.
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
