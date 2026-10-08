package toolchain

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
)

func elfAddon(architecture, tag string) string {
	machine, known := arch.ELFMachine(architecture)
	if !known {
		panic("no ELF machine for " + architecture)
	}
	header := make([]byte, 20)
	copy(header, "\x7fELF")
	header[4], header[5] = 2, 1
	binary.LittleEndian.PutUint16(header[18:20], machine)
	return string(header) + tag
}

type tree map[string]string

func writeTree(t *testing.T, root string, files tree) {
	t.Helper()
	for rel, contents := range files {
		dest := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

type layout struct {
	appSrc      string
	appDir      string
	functionDir string
}

func newLayout(t *testing.T, files tree) layout {
	t.Helper()
	root := t.TempDir()
	appSrc := filepath.Join(root, "src")
	writeTree(t, appSrc, files)
	appDir := filepath.Join(root, "out", "apps", "api")
	return layout{appSrc: appSrc, appDir: appDir, functionDir: filepath.Join(appDir, "functions", "index.func")}
}

func (l layout) target(entry string) Target {
	return Target{
		App:         "api",
		Framework:   buildoutput.Framework{Name: "node"},
		Entrypoint:  filepath.Join(l.appSrc, filepath.FromSlash(entry)),
		FunctionDir: l.functionDir,
		AppDir:      l.appDir,
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func runNode(t *testing.T, functionDir string) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	out, err := exec.Command("node", filepath.Join(functionDir, handlerFile)).CombinedOutput()
	if err != nil {
		t.Fatalf("node %s: %v\n%s", handlerFile, err, out)
	}
	return string(out)
}

const appPkg = `{"name":"api","type":"module"}`

func TestBundle(t *testing.T) {
	t.Parallel()

	t.Run("emits one module, function-config.json and hosting.json", func(t *testing.T) {
		t.Parallel()

		l := newLayout(t, tree{
			"package.json":                      appPkg,
			"server.js":                         "import { tag } from './lib.js';\nimport cjs from 'cjs-dep';\nconsole.log(tag + cjs.mark);\nexport default {};\n",
			"lib.js":                            "export const tag = 'lib:';\n",
			"node_modules/cjs-dep/package.json": `{"name":"cjs-dep","main":"index.js"}`,
			"node_modules/cjs-dep/index.js":     "module.exports = { mark: 'cjs' };\n",
		})

		if err := Bundle(context.Background(), l.target("server.js")); err != nil {
			t.Fatalf("Bundle: %v", err)
		}

		entries, err := os.ReadDir(l.functionDir)
		if err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		if len(names) != 2 {
			t.Errorf("function directory contains %v, want only the bundle and %s", names, buildoutput.FunctionConfigFile)
		}

		var cfg buildoutput.FunctionConfig
		if err := json.Unmarshal([]byte(readFile(t, filepath.Join(l.functionDir, buildoutput.FunctionConfigFile))), &cfg); err != nil {
			t.Fatal(err)
		}
		want := buildoutput.FunctionConfig{Framework: buildoutput.Framework{Name: "node"}, EntryFile: handlerFile, ID: rootFunctionRouteID, App: "api"}
		if !reflect.DeepEqual(cfg, want) {
			t.Errorf("%s = %+v, want %+v", buildoutput.FunctionConfigFile, cfg, want)
		}

		var hosting buildoutput.Hosting
		hostingPath := filepath.Join(l.appDir, buildoutput.HostingFile)
		if err := json.Unmarshal([]byte(readFile(t, hostingPath)), &hosting); err != nil {
			t.Fatal(err)
		}
		if hosting.Framework != "node" {
			t.Errorf("%s runtime = %q, want node", buildoutput.HostingFile, hosting.Framework)
		}
		if len(hosting.FrameworkBuildID) != buildIDLength {
			t.Errorf("%s frameworkBuildId = %q, want %d hex characters", buildoutput.HostingFile, hosting.FrameworkBuildID, buildIDLength)
		}
		if hosting.Needs == nil {
			t.Errorf("%s = %s, want needs stated as an empty object, not null", buildoutput.HostingFile, readFile(t, hostingPath))
		}
		if hosting.RootFunction != cfg.ID {
			t.Errorf("%s rootFunction = %q, want the sole function's route id %q", buildoutput.HostingFile, hosting.RootFunction, cfg.ID)
		}
		if _, err := os.Stat(filepath.Join(l.functionDir, buildoutput.HostingFile)); err == nil {
			t.Errorf("%s landed inside the function directory, want it in the app artifact root", buildoutput.HostingFile)
		}

		if got := runNode(t, l.functionDir); !strings.Contains(got, "lib:cjs") {
			t.Errorf("bundle printed %q, want the bundled dependency to answer", got)
		}
	})

	t.Run("a worker entry is bundled beside the handler and named in the config", func(t *testing.T) {
		t.Parallel()

		l := newLayout(t, tree{
			"package.json":                      appPkg,
			"server.js":                         "console.log('server');\n",
			"tasks.js":                          "import dep from 'cjs-dep';\nexport const shape = dep.shape;\n",
			"node_modules/cjs-dep/package.json": `{"name":"cjs-dep","main":"index.js"}`,
			"node_modules/cjs-dep/index.js":     "module.exports = { shape: 'worker:' + typeof __dirname };\n",
		})
		target := l.target("server.js")
		target.WorkerSource = "const { shape } = await import(" + strconv.Quote(filepath.Join(l.appSrc, "tasks.js")) + ");\nconsole.log(shape);\n"
		target.WorkerResolveDir = l.appSrc

		if err := Bundle(context.Background(), target); err != nil {
			t.Fatalf("Bundle: %v", err)
		}
		var cfg buildoutput.FunctionConfig
		if err := json.Unmarshal([]byte(readFile(t, filepath.Join(l.functionDir, buildoutput.FunctionConfigFile))), &cfg); err != nil {
			t.Fatal(err)
		}
		if want := []string{"node", workerFile}; !reflect.DeepEqual(cfg.Worker, want) {
			t.Errorf("%s worker = %v, want %v", buildoutput.FunctionConfigFile, cfg.Worker, want)
		}
		if _, err := exec.LookPath("node"); err != nil {
			t.Skip("node not on PATH")
		}
		out, err := exec.Command("node", filepath.Join(l.functionDir, workerFile)).CombinedOutput()
		if err != nil || !strings.Contains(string(out), "worker:string") {
			t.Errorf("the worker entry printed %q (%v), want the bundled declarations to run", out, err)
		}
	})

	t.Run("a bundled dependency still sees __dirname and require", func(t *testing.T) {
		t.Parallel()

		l := newLayout(t, tree{
			"package.json":                      appPkg,
			"server.js":                         "import dep from 'cjs-dep';\nconsole.log(dep.shape);\n",
			"node_modules/cjs-dep/package.json": `{"name":"cjs-dep","main":"index.js"}`,
			"node_modules/cjs-dep/index.js": "const path = require('node:path');\n" +
				"module.exports = { shape: typeof __dirname + ':' + typeof __filename + ':' + typeof path.join };\n",
		})

		if err := Bundle(context.Background(), l.target("server.js")); err != nil {
			t.Fatalf("Bundle: %v", err)
		}
		if got, want := runNode(t, l.functionDir), "string:string:function"; !strings.Contains(got, want) {
			t.Errorf("bundle printed %q, want %q", got, want)
		}
	})

	t.Run("a native addon stays external and is copied beside the bundle", func(t *testing.T) {
		t.Parallel()

		l := newLayout(t, tree{
			"package.json":                                     appPkg,
			"server.js":                                        "import native from 'native-dep';\nconsole.log(native);\n",
			"node_modules/native-dep/package.json":             `{"name":"native-dep","main":"index.js"}`,
			"node_modules/native-dep/index.js":                 "module.exports = require('./build/Release/addon.node');\n",
			"node_modules/native-dep/build/Release/addon.node": elfAddon(arch.X8664, "fake"),
		})

		if err := Bundle(context.Background(), l.target("server.js")); err != nil {
			t.Fatalf("Bundle: %v", err)
		}

		copied := filepath.Join(l.functionDir, "node_modules", "native-dep", "build", "Release", "addon.node")
		if got := readFile(t, copied); got != elfAddon(arch.X8664, "fake") {
			t.Errorf("copied addon = %q, want the original bytes", got)
		}
		bundle := readFile(t, filepath.Join(l.functionDir, handlerFile))
		if !strings.Contains(bundle, `"./node_modules/native-dep/build/Release/addon.node"`) {
			t.Errorf("bundle does not require the copied addon by its output path:\n%s", bundle)
		}
	})

	t.Run("every native addon in the graph is copied", func(t *testing.T) {
		t.Parallel()

		const count = 24
		files := tree{"package.json": appPkg}
		var imports, logs strings.Builder
		for i := range count {
			name := fmt.Sprintf("native-dep-%02d", i)
			files["node_modules/"+name+"/package.json"] = `{"name":"` + name + `","main":"index.js"}`
			files["node_modules/"+name+"/index.js"] = "module.exports = require('./build/Release/addon.node');\n"
			files["node_modules/"+name+"/build/Release/addon.node"] = elfAddon(arch.X8664, name)
			fmt.Fprintf(&imports, "import n%02d from '%s';\n", i, name)
			fmt.Fprintf(&logs, "console.log(n%02d);\n", i)
		}
		files["server.js"] = imports.String() + logs.String()
		l := newLayout(t, files)

		if err := Bundle(context.Background(), l.target("server.js")); err != nil {
			t.Fatalf("Bundle: %v", err)
		}

		for i := range count {
			name := fmt.Sprintf("native-dep-%02d", i)
			copied := filepath.Join(l.functionDir, "node_modules", name, "build", "Release", "addon.node")
			if got, want := readFile(t, copied), elfAddon(arch.X8664, name); got != want {
				t.Errorf("copied addon = %q, want %q", got, want)
			}
		}
	})

	t.Run("a runtime addon loader fails the build", func(t *testing.T) {
		t.Parallel()

		for _, loader := range addonLoaders {
			t.Run(loader, func(t *testing.T) {
				t.Parallel()

				l := newLayout(t, tree{
					"package.json":                             appPkg,
					"server.js":                                "import native from 'native-dep';\nconsole.log(native);\n",
					"node_modules/native-dep/package.json":     `{"name":"native-dep","main":"index.js"}`,
					"node_modules/native-dep/index.js":         "module.exports = require('" + loader + "')('native_dep.node');\n",
					"node_modules/" + loader + "/package.json": `{"name":"` + loader + `","main":"index.js"}`,
					"node_modules/" + loader + "/index.js":     "module.exports = () => ({});\n",
				})

				err := Bundle(context.Background(), l.target("server.js"))
				if err == nil {
					t.Fatal("Bundle succeeded, want a build error rather than a function that dies at cold start")
				}
				for _, want := range []string{loader, "native-dep", "OCEL_BUILD_PREFER_TRACING"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error = %q, want it to name %q", err, want)
					}
				}
			})
		}
	})

	t.Run("an addon loader nothing imports does not block the build", func(t *testing.T) {
		t.Parallel()

		l := newLayout(t, tree{
			"package.json":                        `{"name":"api","type":"module","dependencies":{"bindings":"^1.5.0"}}`,
			"server.js":                           "import { tag } from 'plain-dep';\nconsole.log(tag);\n",
			"node_modules/plain-dep/package.json": `{"name":"plain-dep","main":"index.js"}`,
			"node_modules/plain-dep/index.js":     "exports.tag = 'plain';\n",
			"node_modules/bindings/package.json":  `{"name":"bindings","main":"index.js"}`,
			"node_modules/bindings/index.js":      "module.exports = () => ({});\n",
		})

		if err := Bundle(context.Background(), l.target("server.js")); err != nil {
			t.Fatalf("Bundle: %v (a dependency the entrypoint never reaches must not block the build)", err)
		}
		if got := runNode(t, l.functionDir); !strings.Contains(got, "plain") {
			t.Errorf("bundle printed %q, want the reachable dependency to answer", got)
		}
	})

	t.Run("a package merely named like a loader is left alone", func(t *testing.T) {
		t.Parallel()

		l := newLayout(t, tree{
			"package.json": appPkg,
			"server.js":    "import { tag } from 'bindings-lite';\nconsole.log(tag);\n",
			"node_modules/bindings-lite/package.json": `{"name":"bindings-lite","main":"index.js"}`,
			"node_modules/bindings-lite/index.js":     "exports.tag = 'lite';\n",
		})

		if err := Bundle(context.Background(), l.target("server.js")); err != nil {
			t.Fatalf("Bundle: %v", err)
		}
		if got := runNode(t, l.functionDir); !strings.Contains(got, "lite") {
			t.Errorf("bundle printed %q, want the dependency to answer", got)
		}
	})

	t.Run("the build id covers the whole function directory", func(t *testing.T) {
		t.Parallel()

		build := func(t *testing.T, extra string) string {
			t.Helper()
			l := newLayout(t, tree{
				"package.json": appPkg,
				"server.js":    "console.log('hi');" + extra,
			})
			if err := Bundle(context.Background(), l.target("server.js")); err != nil {
				t.Fatalf("Bundle: %v", err)
			}
			var hosting buildoutput.Hosting
			if err := json.Unmarshal([]byte(readFile(t, filepath.Join(l.appDir, buildoutput.HostingFile))), &hosting); err != nil {
				t.Fatal(err)
			}
			return hosting.FrameworkBuildID
		}

		first, again := build(t, ""), build(t, "")
		if first != again {
			t.Errorf("build id = %q then %q, want the same bytes to hash the same", first, again)
		}
		if changed := build(t, "console.log('there');"); changed == first {
			t.Errorf("build id stayed %q after the bundle changed", changed)
		}
	})

	failures := []struct {
		name  string
		files tree
		entry string
		mut   func(*Target)
		wants []string
	}{
		{
			name:  "a missing native addon fails the build",
			files: tree{"package.json": appPkg, "server.js": "const addon = require('./build/addon.node');\nconsole.log(addon);\n"},
			entry: "server.js",
			wants: []string{"addon.node", "OCEL_BUILD_PREFER_TRACING"},
		},
		{
			name:  "an unresolvable import fails the build",
			files: tree{"package.json": appPkg, "server.js": "import 'nowhere-at-all';\n"},
			entry: "server.js",
			wants: []string{"nowhere-at-all"},
		},
		{
			name:  "a syntax error fails the build",
			files: tree{"package.json": appPkg, "server.js": "export default {;\n"},
			entry: "server.js",
			wants: []string{"server.js"},
		},
		{
			name:  "an entrypoint that is not there fails before esbuild runs",
			files: tree{"package.json": appPkg},
			entry: "server.js",
			wants: []string{"server.js"},
		},
		{
			name:  "an unnamed framework fails the build",
			files: tree{"package.json": appPkg, "server.js": "console.log('hi');\n"},
			entry: "server.js",
			mut:   func(target *Target) { target.Framework = buildoutput.Framework{} },
			wants: []string{"framework"},
		},
		{
			name:  "an unstated app fails the build",
			files: tree{"package.json": appPkg, "server.js": "console.log('hi');\n"},
			entry: "server.js",
			mut:   func(target *Target) { target.App = "" },
			wants: []string{"app"},
		},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			l := newLayout(t, tt.files)
			target := l.target(tt.entry)
			if tt.mut != nil {
				tt.mut(&target)
			}

			err := Bundle(context.Background(), target)
			if err == nil {
				t.Fatal("Bundle succeeded, want a build error rather than a function that breaks at cold start")
			}
			for _, want := range tt.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to name %q", err, want)
				}
			}
		})
	}
}

func TestNodeEntrypoint(t *testing.T) {
	t.Parallel()

	t.Run("finds the first candidate present", func(t *testing.T) {
		t.Parallel()

		source := t.TempDir()
		writeTree(t, source, tree{"index.js": "", "src/app.ts": ""})
		got, err := NodeEntrypoint(source, "")
		if err != nil {
			t.Fatalf("NodeEntrypoint: %v", err)
		}
		if want := filepath.Join(source, "src", "app.ts"); got != want {
			t.Errorf("NodeEntrypoint = %q, want %q", got, want)
		}
	})

	t.Run("honours a declared entrypoint over every candidate", func(t *testing.T) {
		t.Parallel()

		source := t.TempDir()
		writeTree(t, source, tree{"src/server.ts": "", "worker/main.ts": ""})
		got, err := NodeEntrypoint(source, "worker/main.ts")
		if err != nil {
			t.Fatalf("NodeEntrypoint: %v", err)
		}
		if want := filepath.Join(source, "worker", "main.ts"); got != want {
			t.Errorf("NodeEntrypoint = %q, want %q", got, want)
		}
	})

	t.Run("names a declared entrypoint that is not there", func(t *testing.T) {
		t.Parallel()

		_, err := NodeEntrypoint(t.TempDir(), "worker/main.ts")
		if err == nil || !strings.Contains(err.Error(), "worker/main.ts") {
			t.Errorf("NodeEntrypoint err = %v, want it to name the declared entrypoint", err)
		}
	})

	t.Run("names every candidate when none is there", func(t *testing.T) {
		t.Parallel()

		_, err := NodeEntrypoint(t.TempDir(), "")
		if err == nil || !strings.Contains(err.Error(), "src/server.ts") || !strings.Contains(err.Error(), "app.js") {
			t.Errorf("NodeEntrypoint err = %v, want it to name the candidates tried", err)
		}
	})
}
