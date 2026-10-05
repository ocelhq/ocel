package toolchain

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/arch"
)

const sharpManifest = `{"name":"sharp","version":"0.34.5","main":"lib/index.js","optionalDependencies":{` +
	`"@img/sharp-darwin-arm64":"0.34.5","@img/sharp-libvips-darwin-arm64":"1.2.4",` +
	`"@img/sharp-linux-x64":"0.34.5","@img/sharp-libvips-linux-x64":"1.2.4",` +
	`"@img/sharp-linux-arm64":"0.34.5","@img/sharp-libvips-linux-arm64":"1.2.4"}}`

func sharpVariant(name, version, os, cpu, libc string) string {
	if libc == "" {
		return fmt.Sprintf(`{"name":%q,"version":%q,"os":[%q],"cpu":[%q]}`, name, version, os, cpu)
	}
	return fmt.Sprintf(`{"name":%q,"version":%q,"os":[%q],"cpu":[%q],"libc":[%q]}`, name, version, os, cpu, libc)
}

func tracedOnAMac() tree {
	return tree{
		"node_modules/sharp/package.json":                           sharpManifest,
		"node_modules/sharp/lib/index.js":                           "module.exports = require('@img/sharp-darwin-arm64/sharp.node');\n",
		"node_modules/@img/sharp-darwin-arm64/package.json":         sharpVariant("@img/sharp-darwin-arm64", "0.34.5", "darwin", "arm64", ""),
		"node_modules/@img/sharp-darwin-arm64/lib/sharp.node":       "darwin binary",
		"node_modules/@img/sharp-libvips-darwin-arm64/package.json": sharpVariant("@img/sharp-libvips-darwin-arm64", "1.2.4", "darwin", "arm64", ""),
	}
}

func linuxSharpInstall(t *testing.T, cpu string) string {
	t.Helper()
	return stagedInstall(t, tree{
		"node_modules/sharp/package.json":                                      sharpManifest,
		"node_modules/detect-libc/package.json":                                `{"name":"detect-libc","version":"2.1.2"}`,
		"node_modules/@img/sharp-linux-" + cpu + "/package.json":               sharpVariant("@img/sharp-linux-"+cpu, "0.34.5", "linux", cpu, "glibc"),
		"node_modules/@img/sharp-linux-" + cpu + "/lib/sharp.node":             "linux binary",
		"node_modules/@img/sharp-libvips-linux-" + cpu + "/package.json":       sharpVariant("@img/sharp-libvips-linux-"+cpu, "1.2.4", "linux", cpu, "glibc"),
		"node_modules/@img/sharp-libvips-linux-" + cpu + "/lib/libvips-cpp.so": "linux library",
	})
}

func installTraced(t *testing.T, functionDir, architecture string) error {
	t.Helper()
	cache := NewPlatformVariantCache()
	t.Cleanup(func() { _ = cache.Close() })
	return cache.InstallTraced(context.Background(), "web", t.TempDir(), functionDir, architecture)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestATracedFunctionShipsSharpBuiltForItsLinuxTargetInsteadOfTheBuildHosts(t *testing.T) {
	for architecture, cpu := range map[string]string{arch.X8664: "x64", arch.ARM64: "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			npm := installFakeNpm(t, linuxSharpInstall(t, cpu))
			functionDir := t.TempDir()
			writeTree(t, functionDir, tracedOnAMac())

			if err := installTraced(t, functionDir, architecture); err != nil {
				t.Fatalf("InstallTraced: %v", err)
			}

			argv := " " + strings.Join(strings.Fields(readFile(t, npm.argv)), " ") + " "
			for _, want := range []string{"install", "--os=linux", "--cpu=" + cpu, "--libc=glibc", "--ignore-scripts"} {
				if !strings.Contains(argv, " "+want+" ") {
					t.Errorf("npm is run as %q, with no %q", argv, want)
				}
			}
			var manifest struct {
				Dependencies map[string]string `json:"dependencies"`
			}
			if err := json.Unmarshal([]byte(readFile(t, npm.manifest)), &manifest); err != nil {
				t.Fatal(err)
			}
			if manifest.Dependencies["sharp"] != "0.34.5" || len(manifest.Dependencies) != 1 {
				t.Errorf("npm installs %v, want only sharp at the version the function traced", manifest.Dependencies)
			}
			for _, want := range []string{"@img/sharp-linux-" + cpu + "/lib/sharp.node", "@img/sharp-libvips-linux-" + cpu + "/lib/libvips-cpp.so"} {
				if !exists(filepath.Join(functionDir, "node_modules", filepath.FromSlash(want))) {
					t.Errorf("the function has no node_modules/%s beside sharp, so sharp's first require fails on linux/%s", want, cpu)
				}
			}
			for _, gone := range []string{"@img/sharp-darwin-arm64", "@img/sharp-libvips-darwin-arm64", "detect-libc"} {
				if exists(filepath.Join(functionDir, "node_modules", filepath.FromSlash(gone))) {
					t.Errorf("the function still ships node_modules/%s, which nothing on linux/%s loads", gone, cpu)
				}
			}
		})
	}
}

func TestATracedPnpmFunctionGetsSharpsLinuxBuildBesideTheSharpItTraced(t *testing.T) {
	installFakeNpm(t, linuxSharpInstall(t, "x64"))
	functionDir := t.TempDir()
	store := "node_modules/.pnpm/sharp@0.34.5/node_modules"
	variant := "node_modules/.pnpm/@img+sharp-darwin-arm64@0.34.5/node_modules/@img/sharp-darwin-arm64"
	writeTree(t, functionDir, tree{
		store + "/sharp/package.json": sharpManifest,
		variant + "/package.json":     sharpVariant("@img/sharp-darwin-arm64", "0.34.5", "darwin", "arm64", ""),
		variant + "/lib/sharp.node":   "darwin binary",
		store + "/@img/.keep":         "",
	})
	link := filepath.Join(functionDir, filepath.FromSlash(store), "@img", "sharp-darwin-arm64")
	if err := os.Symlink("../../../@img+sharp-darwin-arm64@0.34.5/node_modules/@img/sharp-darwin-arm64", link); err != nil {
		t.Fatal(err)
	}

	if err := installTraced(t, functionDir, arch.X8664); err != nil {
		t.Fatalf("InstallTraced: %v", err)
	}

	if !exists(filepath.Join(functionDir, filepath.FromSlash(store), "@img", "sharp-linux-x64", "lib", "sharp.node")) {
		t.Errorf("the linux build is not in %s/@img, where sharp in the pnpm store resolves it from", store)
	}
	if exists(link) {
		t.Error("the store still links the darwin build, a link to nothing once that build is gone")
	}
	if exists(filepath.Join(functionDir, filepath.FromSlash(variant))) {
		t.Error("the function still ships the darwin build")
	}
}

func TestATracedFunctionThatAlreadyHoldsTheTargetsBuildInstallsNothing(t *testing.T) {
	npm := installFakeNpm(t, "exit 1\n")
	functionDir := t.TempDir()
	writeTree(t, functionDir, tree{
		"node_modules/sharp/package.json":                        sharpManifest,
		"node_modules/@img/sharp-linux-x64/package.json":         sharpVariant("@img/sharp-linux-x64", "0.34.5", "linux", "x64", "glibc"),
		"node_modules/@img/sharp-libvips-linux-x64/package.json": sharpVariant("@img/sharp-libvips-linux-x64", "1.2.4", "linux", "x64", "glibc"),
		"node_modules/@img/sharp-linuxmusl-x64/package.json":     sharpVariant("@img/sharp-linuxmusl-x64", "0.34.5", "linux", "x64", "musl"),
	})

	if err := installTraced(t, functionDir, arch.X8664); err != nil {
		t.Fatalf("InstallTraced: %v", err)
	}
	if exists(npm.argv) {
		t.Error("npm ran for a function that already holds sharp's build for its target")
	}
	if !exists(filepath.Join(functionDir, "node_modules", "@img", "sharp-linux-x64")) {
		t.Error("the target's own build was removed")
	}
	if exists(filepath.Join(functionDir, "node_modules", "@img", "sharp-linuxmusl-x64")) {
		t.Error("the function still ships the musl build, which a glibc host never loads")
	}
}

func TestAPackageWhosePlatformBuildsTheTraceNeverReachedInstallsNothing(t *testing.T) {
	npm := installFakeNpm(t, "exit 1\n")
	functionDir := t.TempDir()
	writeTree(t, functionDir, tree{
		"node_modules/next/package.json":   `{"name":"next","version":"16.0.0","optionalDependencies":{"@next/swc-darwin-arm64":"16.0.0","@next/swc-linux-x64-gnu":"16.0.0"}}`,
		"node_modules/next/dist/server.js": "",
	})

	if err := installTraced(t, functionDir, arch.X8664); err != nil {
		t.Fatalf("InstallTraced: %v", err)
	}
	if exists(npm.argv) {
		t.Error("npm ran for next, whose platform builds the function never loads")
	}
}

func TestATracedPlatformInstallThatBringsNoBuildForTheTargetFailsTheBuild(t *testing.T) {
	installFakeNpm(t, linuxSharpInstall(t, "arm64"))
	functionDir := t.TempDir()
	writeTree(t, functionDir, tracedOnAMac())

	err := installTraced(t, functionDir, arch.X8664)
	if err == nil {
		t.Fatal("InstallTraced succeeded, want a refusal rather than a function whose first require of sharp fails")
	}
	for _, want := range []string{"sharp", "web", "linux/x64"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
}

func TestAFailedTracedPlatformInstallNamesTheProjectNpmrcItCopiedWithoutClaimingARegistry(t *testing.T) {
	installFakeNpm(t, "echo 'E401 Unable to authenticate'\nexit 1\n")
	functionDir := t.TempDir()
	writeTree(t, functionDir, tracedOnAMac())
	source := t.TempDir()
	writeTree(t, source, tree{".npmrc": "registry=https://npm.internal.example/\n"})
	cache := NewPlatformVariantCache()
	t.Cleanup(func() { _ = cache.Close() })

	err := cache.InstallTraced(context.Background(), "web", source, functionDir, arch.X8664)
	if err == nil {
		t.Fatal("InstallTraced succeeded, want npm's failure reported")
	}
	config := filepath.Join(source, ".npmrc")
	for _, want := range []string{config, "copied"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to say it copied the project's npm config %s into the install", err, want)
		}
	}
	for _, unwanted := range []string{"default registry", "only package manager config"} {
		if strings.Contains(err.Error(), unwanted) {
			t.Errorf("error = %q, want no claim %q: npm also reads its environment and user and global config", err, unwanted)
		}
	}
}

func TestAFailedTracedPlatformInstallWithoutAProjectNpmrcSaysNpmUsedItsOwnConfiguration(t *testing.T) {
	installFakeNpm(t, "echo 'E401 Unable to authenticate'\nexit 1\n")
	functionDir := t.TempDir()
	writeTree(t, functionDir, tracedOnAMac())

	err := installTraced(t, functionDir, arch.X8664)
	if err == nil {
		t.Fatal("InstallTraced succeeded, want npm's failure reported")
	}
	for _, want := range []string{"no project .npmrc", "user, global and environment"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "default registry") {
		t.Errorf("error = %q, want no claim about which registry npm used", err)
	}
}

func TestATracedPackageInstalledUnderAnNpmAliasGetsItsTargetPlatformVariant(t *testing.T) {
	installFakeNpm(t, linuxSharpInstall(t, "x64"))
	functionDir := t.TempDir()
	writeTree(t, functionDir, tree{
		"node_modules/img/package.json":                       sharpManifest,
		"node_modules/@img/sharp-darwin-arm64/package.json":   sharpVariant("@img/sharp-darwin-arm64", "0.34.5", "darwin", "arm64", ""),
		"node_modules/@img/sharp-darwin-arm64/lib/sharp.node": "darwin binary",
	})

	if err := installTraced(t, functionDir, arch.X8664); err != nil {
		t.Fatalf("InstallTraced: %v", err)
	}

	if !exists(filepath.Join(functionDir, "node_modules", "@img", "sharp-linux-x64", "lib", "sharp.node")) {
		t.Error("the aliased sharp has no linux build beside it, so its first require fails")
	}
	if exists(filepath.Join(functionDir, "node_modules", "@img", "sharp-darwin-arm64")) {
		t.Error("the function still ships the darwin build")
	}
}

func TestAPackageJsonNestedInsideAPackageIsNotReadAsAPackage(t *testing.T) {
	functionDir := t.TempDir()
	writeTree(t, functionDir, tree{
		"node_modules/foo/package.json":     `{"name":"foo","version":"1.0.0"}`,
		"node_modules/foo/esm/package.json": `{"name":"foo","version":"1.0.0"}`,
	})

	traced, err := readTracedTree(functionDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(traced.packages) != 1 || traced.packages[0].root != filepath.Join(functionDir, "node_modules", "foo") {
		t.Errorf("packages = %v, want only node_modules/foo", traced.packages)
	}
}

func TestABuildInstallsATracedPackagesLinuxBuildOnceForEveryFunctionThatTracesIt(t *testing.T) {
	runs := filepath.Join(t.TempDir(), "runs")
	installFakeNpm(t, fmt.Sprintf("echo run >> %q\n", runs)+linuxSharpInstall(t, "x64"))
	cache := NewPlatformVariantCache()
	t.Cleanup(func() { _ = cache.Close() })
	source := t.TempDir()

	functionDirs := []string{t.TempDir(), t.TempDir(), t.TempDir()}
	for _, functionDir := range functionDirs {
		writeTree(t, functionDir, tracedOnAMac())
		if err := cache.InstallTraced(context.Background(), "web", source, functionDir, arch.X8664); err != nil {
			t.Fatalf("InstallTraced: %v", err)
		}
	}

	if got := strings.Count(readFile(t, runs), "run"); got != 1 {
		t.Errorf("npm ran %d times for three functions tracing the same sharp, want once", got)
	}
	for _, functionDir := range functionDirs {
		if !exists(filepath.Join(functionDir, "node_modules", "@img", "sharp-linux-x64", "lib", "sharp.node")) {
			t.Errorf("%s has no linux build of sharp", functionDir)
		}
	}
}
