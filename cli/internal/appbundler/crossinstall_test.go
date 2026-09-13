package appbundler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type fakeNpm struct {
	argv     string
	manifest string
}

func installFakeNpm(t *testing.T, body string) fakeNpm {
	t.Helper()
	bin := t.TempDir()
	record := t.TempDir()
	npm := fakeNpm{argv: filepath.Join(record, "argv"), manifest: filepath.Join(record, "package.json")}
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\ncp package.json %q\n%s", npm.argv, npm.manifest, body)
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return npm
}

func platformSplitApp() tree {
	return tree{
		"package.json": appPkg,
		"server.js":    "import answer from 'plat-dep';\nconsole.log(answer);\n",
		"node_modules/plat-dep/package.json": `{"name":"plat-dep","version":"1.2.3","main":"index.js",` +
			`"optionalDependencies":{"@plat-dep/darwin-arm64":"1.2.3","@plat-dep/linux-x64":"1.2.3","@plat-dep/linux-arm64":"1.2.3"}}`,
		"node_modules/plat-dep/index.js":                   "module.exports = require('@plat-dep/darwin-arm64');\n",
		"node_modules/@plat-dep/darwin-arm64/package.json": `{"name":"@plat-dep/darwin-arm64","version":"1.2.3","os":["darwin"],"cpu":["arm64"],"main":"index.js"}`,
		"node_modules/@plat-dep/darwin-arm64/index.js":     "module.exports = 'host build';\n",
	}
}

const skippedOptional = "npm warn skipping optional dependency for another platform"

func stagedInstall(t *testing.T, files tree) string {
	t.Helper()
	staged := t.TempDir()
	writeTree(t, staged, files)
	return fmt.Sprintf("cp -R %q/. .\necho '%s'\n", staged, skippedOptional)
}

func stagedPlatDep() tree {
	return tree{
		"node_modules/plat-dep/package.json": `{"name":"plat-dep","version":"1.2.3","main":"index.js",` +
			`"optionalDependencies":{"@plat-dep/darwin-arm64":"1.2.3","@plat-dep/linux-x64":"1.2.3","@plat-dep/linux-arm64":"1.2.3"}}`,
		"node_modules/plat-dep/index.js": "module.exports = 'target build';\n",
	}
}

func linuxInstall(t *testing.T, cpu, libc string) string {
	t.Helper()
	files := stagedPlatDep()
	files["node_modules/@plat-dep/linux-"+cpu+"/package.json"] = fmt.Sprintf(
		`{"name":"@plat-dep/linux-%[1]s","version":"1.2.3","os":["linux"],"cpu":["%[1]s"],"libc":["%[2]s"]}`, cpu, libc)
	return stagedInstall(t, files)
}

func TestAPackageShippingOnePackagePerPlatformIsInstalledForTheDeclaredArchitecture(t *testing.T) {
	for arch, cpu := range map[string]string{providerkit.ArchX8664: "x64", providerkit.ArchARM64: "arm64"} {
		t.Run(arch, func(t *testing.T) {
			npm := installFakeNpm(t, linuxInstall(t, cpu, "glibc"))
			l := newLayout(t, platformSplitApp())
			target := l.target("server.js")
			target.Runtime.Arch = arch

			if err := Bundle(context.Background(), target); err != nil {
				t.Fatalf("Bundle: %v", err)
			}

			argv := strings.Fields(readFile(t, npm.argv))
			for _, want := range []string{"install", "--os=linux", "--cpu=" + cpu, "--libc=glibc", "--ignore-scripts"} {
				if !strings.Contains(" "+strings.Join(argv, " ")+" ", " "+want+" ") {
					t.Errorf("npm is run as %v, and it carries no %q: the build host's own platform package is what gets installed", argv, want)
				}
			}
			var manifest struct {
				Dependencies map[string]string `json:"dependencies"`
			}
			if err := json.Unmarshal([]byte(readFile(t, npm.manifest)), &manifest); err != nil {
				t.Fatal(err)
			}
			if manifest.Dependencies["plat-dep"] != "1.2.3" || len(manifest.Dependencies) != 1 {
				t.Errorf("npm installs %v, want only plat-dep at the version the app installed", manifest.Dependencies)
			}
			if got := runNode(t, l.funcDir); !strings.Contains(got, "target build") {
				t.Errorf("bundle printed %q, want the package installed for the target to answer", got)
			}
		})
	}
}

func TestADependencyThatDoesNotSplitByPlatformIsBundledWithoutAnInstall(t *testing.T) {
	npm := installFakeNpm(t, "exit 1\n")
	l := newLayout(t, tree{
		"package.json":                             appPkg,
		"server.js":                                "import watch from 'watch-dep';\nconsole.log(watch);\n",
		"node_modules/watch-dep/package.json":      `{"name":"watch-dep","version":"3.0.0","main":"index.js","optionalDependencies":{"absent-dep":"1.0.0","plain-optional":"1.0.0"}}`,
		"node_modules/watch-dep/index.js":          "module.exports = 'bundled';\n",
		"node_modules/plain-optional/package.json": `{"name":"plain-optional","version":"1.0.0"}`,
	})

	if err := Bundle(context.Background(), l.target("server.js")); err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if _, err := os.Stat(npm.argv); err == nil {
		t.Error("npm ran for an app whose dependencies install the same on every platform")
	}
	if got := runNode(t, l.funcDir); !strings.Contains(got, "bundled") {
		t.Errorf("bundle printed %q, want the bundled dependency to answer", got)
	}
}

func TestAPlatformPackageInstallThatFailsFailsTheBuild(t *testing.T) {
	installFakeNpm(t, "echo 'npm error 404 plat-dep@1.2.3 is not in this registry' >&2\nexit 1\n")
	l := newLayout(t, platformSplitApp())

	err := Bundle(context.Background(), l.target("server.js"))
	if err == nil {
		t.Fatal("Bundle succeeded, want a refusal rather than a function carrying the build host's platform package")
	}
	for _, want := range []string{"plat-dep", "not in this registry"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
}

func TestAPlatformPackageWithNoNpmToInstallItFailsTheBuild(t *testing.T) {
	t.Setenv("PATH", "")
	l := newLayout(t, platformSplitApp())

	err := Bundle(context.Background(), l.target("server.js"))
	if err == nil {
		t.Fatal("Bundle succeeded, want a refusal rather than a function carrying the build host's platform package")
	}
	for _, want := range []string{"plat-dep", "npm"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
}

func TestAPlatformPackageReachedAtTwoVersionsFailsTheBuild(t *testing.T) {
	npm := installFakeNpm(t, linuxInstall(t, "x64", "glibc"))
	files := platformSplitApp()
	files["server.js"] = "import answer from 'plat-dep';\nimport older from 'wrapper-dep';\nconsole.log(answer, older);\n"
	files["node_modules/wrapper-dep/package.json"] = `{"name":"wrapper-dep","version":"1.0.0","main":"index.js"}`
	files["node_modules/wrapper-dep/index.js"] = "module.exports = require('plat-dep');\n"
	files["node_modules/wrapper-dep/node_modules/plat-dep/package.json"] = `{"name":"plat-dep","version":"0.9.0","main":"index.js",` +
		`"optionalDependencies":{"@plat-dep/darwin-arm64":"0.9.0"}}`
	files["node_modules/wrapper-dep/node_modules/plat-dep/index.js"] = "module.exports = require('@plat-dep/darwin-arm64');\n"
	l := newLayout(t, files)

	err := Bundle(context.Background(), l.target("server.js"))
	if err == nil {
		t.Fatal("Bundle succeeded, want a refusal: one function directory holds one plat-dep, so one of its importers would load a version it was not installed with")
	}
	for _, want := range []string{"plat-dep", "1.2.3", "0.9.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
	if _, err := os.Stat(npm.argv); err == nil {
		t.Error("npm ran for packages the function directory cannot hold side by side")
	}
}

func TestAPlatformPackageInstallStopsWhenTheBuildIsCancelled(t *testing.T) {
	npm := installFakeNpm(t, linuxInstall(t, "x64", "glibc"))
	l := newLayout(t, platformSplitApp())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := Bundle(ctx, l.target("server.js")); err == nil {
		t.Fatal("Bundle succeeded under a cancelled build, want it to stop")
	}
	if _, err := os.Stat(npm.argv); err == nil {
		t.Error("npm ran for a build that was already cancelled")
	}
}

func TestAPlatformPackageInstallThatBringsNoVariantForTheTargetFailsTheBuild(t *testing.T) {
	for name, install := range map[string]func(t *testing.T) string{
		"no variant":            func(t *testing.T) string { return stagedInstall(t, stagedPlatDep()) },
		"a musl variant":        func(t *testing.T) string { return linuxInstall(t, "x64", "musl") },
		"another cpu's variant": func(t *testing.T) string { return linuxInstall(t, "arm64", "glibc") },
	} {
		t.Run(name, func(t *testing.T) {
			installFakeNpm(t, install(t))
			l := newLayout(t, platformSplitApp())

			err := Bundle(context.Background(), l.target("server.js"))
			if err == nil {
				t.Fatal("Bundle succeeded, want a refusal rather than a function that fails its first require")
			}
			for _, want := range []string{"plat-dep", "linux/x64", skippedOptional} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to name %q", err, want)
				}
			}
		})
	}
}

func TestAPackageWhoseOnlyPlatformVariantIsForAnotherOSInstallsWithoutOne(t *testing.T) {
	installFakeNpm(t, stagedInstall(t, tree{
		"node_modules/watch-dep/package.json": `{"name":"watch-dep","version":"3.0.0","main":"index.js","optionalDependencies":{"fsevents":"2.3.3"}}`,
		"node_modules/watch-dep/index.js":     "module.exports = 'target build';\n",
	}))
	l := newLayout(t, tree{
		"package.json":                        appPkg,
		"server.js":                           "import watch from 'watch-dep';\nconsole.log(watch);\n",
		"node_modules/watch-dep/package.json": `{"name":"watch-dep","version":"3.0.0","main":"index.js","optionalDependencies":{"fsevents":"2.3.3"}}`,
		"node_modules/watch-dep/index.js":     "module.exports = 'host build';\n",
		"node_modules/fsevents/package.json":  `{"name":"fsevents","version":"2.3.3","os":["darwin"]}`,
	})

	if err := Bundle(context.Background(), l.target("server.js")); err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if got := runNode(t, l.funcDir); !strings.Contains(got, "target build") {
		t.Errorf("bundle printed %q, want the package installed for the target to answer", got)
	}
}

func TestAPlatformPackageWhoseHostVariantWasNeverInstalledIsStillInstalledForTheTarget(t *testing.T) {
	npm := installFakeNpm(t, linuxInstall(t, "x64", "glibc"))
	files := platformSplitApp()
	delete(files, "node_modules/@plat-dep/darwin-arm64/package.json")
	delete(files, "node_modules/@plat-dep/darwin-arm64/index.js")
	files["node_modules/plat-dep/index.js"] = "module.exports = 'host build';\n"
	l := newLayout(t, files)

	if err := Bundle(context.Background(), l.target("server.js")); err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if _, err := os.Stat(npm.argv); err != nil {
		t.Error("npm never ran: a host that installed with --omit=optional, or has no published variant, ships plat-dep with no binary")
	}
	if got := runNode(t, l.funcDir); !strings.Contains(got, "target build") {
		t.Errorf("bundle printed %q, want the package installed for the target to answer", got)
	}
}

func TestAPlatformPackageImportedThroughAnAliasIsInstalledUnderTheAlias(t *testing.T) {
	files := stagedPlatDep()
	files["node_modules/img/package.json"] = files["node_modules/plat-dep/package.json"]
	files["node_modules/img/index.js"] = files["node_modules/plat-dep/index.js"]
	delete(files, "node_modules/plat-dep/package.json")
	delete(files, "node_modules/plat-dep/index.js")
	files["node_modules/@plat-dep/linux-x64/package.json"] = `{"name":"@plat-dep/linux-x64","version":"1.2.3","os":["linux"],"cpu":["x64"]}`
	npm := installFakeNpm(t, stagedInstall(t, files))
	app := platformSplitApp()
	app["server.js"] = "import answer from 'img';\nconsole.log(answer);\n"
	app["node_modules/img/package.json"] = app["node_modules/plat-dep/package.json"]
	app["node_modules/img/index.js"] = app["node_modules/plat-dep/index.js"]
	delete(app, "node_modules/plat-dep/package.json")
	delete(app, "node_modules/plat-dep/index.js")
	l := newLayout(t, app)

	if err := Bundle(context.Background(), l.target("server.js")); err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	var manifest struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(readFile(t, npm.manifest)), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Dependencies["img"] != "npm:plat-dep@1.2.3" || len(manifest.Dependencies) != 1 {
		t.Errorf("npm installs %v, want plat-dep under the alias the app imports it by", manifest.Dependencies)
	}
	if got := runNode(t, l.funcDir); !strings.Contains(got, "target build") {
		t.Errorf("bundle printed %q, want the package installed for the target to answer", got)
	}
}
