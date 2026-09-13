package appbundler

import (
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

const linuxInstall = "mkdir -p node_modules/plat-dep\n" +
	"printf '%s' \"module.exports = 'target build';\" > node_modules/plat-dep/index.js\n" +
	"printf '%s' '{\"name\":\"plat-dep\",\"main\":\"index.js\"}' > node_modules/plat-dep/package.json\n"

func TestAPackageShippingOnePackagePerPlatformIsInstalledForTheDeclaredArchitecture(t *testing.T) {
	for arch, cpu := range map[string]string{providerkit.ArchX8664: "x64", providerkit.ArchARM64: "arm64"} {
		t.Run(arch, func(t *testing.T) {
			npm := installFakeNpm(t, linuxInstall)
			l := newLayout(t, platformSplitApp())
			target := l.target("server.js")
			target.Runtime.Arch = arch

			if err := Bundle(target); err != nil {
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

	if err := Bundle(l.target("server.js")); err != nil {
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

	err := Bundle(l.target("server.js"))
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

	err := Bundle(l.target("server.js"))
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
	npm := installFakeNpm(t, linuxInstall)
	files := platformSplitApp()
	files["server.js"] = "import answer from 'plat-dep';\nimport older from 'wrapper-dep';\nconsole.log(answer, older);\n"
	files["node_modules/wrapper-dep/package.json"] = `{"name":"wrapper-dep","version":"1.0.0","main":"index.js"}`
	files["node_modules/wrapper-dep/index.js"] = "module.exports = require('plat-dep');\n"
	files["node_modules/wrapper-dep/node_modules/plat-dep/package.json"] = `{"name":"plat-dep","version":"0.9.0","main":"index.js",` +
		`"optionalDependencies":{"@plat-dep/darwin-arm64":"0.9.0"}}`
	files["node_modules/wrapper-dep/node_modules/plat-dep/index.js"] = "module.exports = require('@plat-dep/darwin-arm64');\n"
	l := newLayout(t, files)

	err := Bundle(l.target("server.js"))
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
