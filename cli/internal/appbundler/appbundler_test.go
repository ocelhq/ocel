package appbundler

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

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
	appSrc  string
	appDir  string
	funcDir string
}

func newLayout(t *testing.T, files tree) layout {
	t.Helper()
	root := t.TempDir()
	appSrc := filepath.Join(root, "src")
	writeTree(t, appSrc, files)
	appDir := filepath.Join(root, "out", "apps", "api")
	return layout{appSrc: appSrc, appDir: appDir, funcDir: filepath.Join(appDir, "functions", "index.func")}
}

func (l layout) target(entry string) Target {
	return Target{
		App:        "api",
		Framework:  "express",
		Runtime:    "nodejs24.x",
		Entrypoint: filepath.Join(l.appSrc, filepath.FromSlash(entry)),
		FuncDir:    l.funcDir,
		AppDir:     l.appDir,
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

func runNode(t *testing.T, funcDir string) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	out, err := exec.Command("node", filepath.Join(funcDir, HandlerFile)).CombinedOutput()
	if err != nil {
		t.Fatalf("node %s: %v\n%s", HandlerFile, err, out)
	}
	return string(out)
}

const appPkg = `{"name":"api","type":"module"}`

func TestBundle(t *testing.T) {
	t.Parallel()

	t.Run("emits one module, a config and a serve descriptor", func(t *testing.T) {
		t.Parallel()

		l := newLayout(t, tree{
			"package.json":                      appPkg,
			"server.js":                         "import { tag } from './lib.js';\nimport cjs from 'cjs-dep';\nconsole.log(tag + cjs.mark);\nexport default {};\n",
			"lib.js":                            "export const tag = 'lib:';\n",
			"node_modules/cjs-dep/package.json": `{"name":"cjs-dep","main":"index.js"}`,
			"node_modules/cjs-dep/index.js":     "module.exports = { mark: 'cjs' };\n",
		})

		if err := Bundle(l.target("server.js")); err != nil {
			t.Fatalf("Bundle: %v", err)
		}

		entries, err := os.ReadDir(l.funcDir)
		if err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		if len(names) != 2 {
			t.Errorf("function directory holds %v, want only the bundle and %s", names, configFileName)
		}

		var cfg functionConfig
		if err := json.Unmarshal([]byte(readFile(t, filepath.Join(l.funcDir, configFileName))), &cfg); err != nil {
			t.Fatal(err)
		}
		want := functionConfig{Runtime: "nodejs24.x", Handler: HandlerFile, Framework: "express", ID: entryRouteID, App: "api"}
		if cfg != want {
			t.Errorf("%s = %+v, want %+v", configFileName, cfg, want)
		}

		var descriptor edge.ServeDescriptor
		descriptorPath := filepath.Join(l.appDir, edge.ServeDescriptorFile)
		if err := json.Unmarshal([]byte(readFile(t, descriptorPath)), &descriptor); err != nil {
			t.Fatal(err)
		}
		if descriptor.Framework != "express" {
			t.Errorf("%s framework = %q, want express", edge.ServeDescriptorFile, descriptor.Framework)
		}
		if len(descriptor.BuildID) != buildIDLength {
			t.Errorf("%s buildId = %q, want %d hex characters", edge.ServeDescriptorFile, descriptor.BuildID, buildIDLength)
		}
		if descriptor.Needs == nil {
			t.Errorf("%s = %s, want needs stated as an empty object, not null", edge.ServeDescriptorFile, readFile(t, descriptorPath))
		}
		if descriptor.Entry != cfg.ID {
			t.Errorf("%s entry = %q, want the sole function's route id %q", edge.ServeDescriptorFile, descriptor.Entry, cfg.ID)
		}
		if _, err := os.Stat(filepath.Join(l.funcDir, edge.ServeDescriptorFile)); err == nil {
			t.Errorf("%s landed inside the function directory, want it in the app artifact root", edge.ServeDescriptorFile)
		}

		if got := runNode(t, l.funcDir); !strings.Contains(got, "lib:cjs") {
			t.Errorf("bundle printed %q, want the bundled dependency to answer", got)
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

		if err := Bundle(l.target("server.js")); err != nil {
			t.Fatalf("Bundle: %v", err)
		}
		if got, want := runNode(t, l.funcDir), "string:string:function"; !strings.Contains(got, want) {
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
			"node_modules/native-dep/build/Release/addon.node": "\x7fELF fake",
		})

		if err := Bundle(l.target("server.js")); err != nil {
			t.Fatalf("Bundle: %v", err)
		}

		copied := filepath.Join(l.funcDir, "node_modules", "native-dep", "build", "Release", "addon.node")
		if got := readFile(t, copied); got != "\x7fELF fake" {
			t.Errorf("copied addon = %q, want the original bytes", got)
		}
		bundle := readFile(t, filepath.Join(l.funcDir, HandlerFile))
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
			files["node_modules/"+name+"/build/Release/addon.node"] = "\x7fELF " + name
			fmt.Fprintf(&imports, "import n%02d from '%s';\n", i, name)
			fmt.Fprintf(&logs, "console.log(n%02d);\n", i)
		}
		files["server.js"] = imports.String() + logs.String()
		l := newLayout(t, files)

		if err := Bundle(l.target("server.js")); err != nil {
			t.Fatalf("Bundle: %v", err)
		}

		for i := range count {
			name := fmt.Sprintf("native-dep-%02d", i)
			copied := filepath.Join(l.funcDir, "node_modules", name, "build", "Release", "addon.node")
			if got, want := readFile(t, copied), "\x7fELF "+name; got != want {
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

				err := Bundle(l.target("server.js"))
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

		if err := Bundle(l.target("server.js")); err != nil {
			t.Fatalf("Bundle: %v (a dependency the entrypoint never reaches must not block the build)", err)
		}
		if got := runNode(t, l.funcDir); !strings.Contains(got, "plain") {
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

		if err := Bundle(l.target("server.js")); err != nil {
			t.Fatalf("Bundle: %v", err)
		}
		if got := runNode(t, l.funcDir); !strings.Contains(got, "lite") {
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
			if err := Bundle(l.target("server.js")); err != nil {
				t.Fatalf("Bundle: %v", err)
			}
			var descriptor edge.ServeDescriptor
			if err := json.Unmarshal([]byte(readFile(t, filepath.Join(l.appDir, edge.ServeDescriptorFile))), &descriptor); err != nil {
				t.Fatal(err)
			}
			return descriptor.BuildID
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
			name:  "a runtime that is not node fails the build",
			files: tree{"package.json": appPkg, "server.js": "console.log('hi');\n"},
			entry: "server.js",
			mut:   func(target *Target) { target.Runtime = "python3.13" },
			wants: []string{"python3.13", "nodejs"},
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

			err := Bundle(target)
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

func TestNodeEngine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		runtime string
		want    string
		wantErr bool
	}{
		{runtime: "nodejs24.x", want: "24"},
		{runtime: "nodejs20.x", want: "20"},
		{runtime: "provided.al2023", wantErr: true},
		{runtime: "nodejs", wantErr: true},
		{runtime: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.runtime, func(t *testing.T) {
			t.Parallel()

			engine, err := nodeEngine(tt.runtime)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("nodeEngine(%q) succeeded, want error", tt.runtime)
				}
				return
			}
			if err != nil {
				t.Fatalf("nodeEngine(%q): %v", tt.runtime, err)
			}
			if engine.Version != tt.want {
				t.Errorf("nodeEngine(%q) = %q, want %q", tt.runtime, engine.Version, tt.want)
			}
		})
	}
}

func TestArtifactHash(t *testing.T) {
	t.Parallel()

	hashOf := func(t *testing.T, files tree, prepare func(t *testing.T, root string)) string {
		t.Helper()
		root := t.TempDir()
		writeTree(t, root, files)
		if prepare != nil {
			prepare(t, root)
		}
		sum, err := artifactHash(root)
		if err != nil {
			t.Fatalf("artifactHash: %v", err)
		}
		return sum
	}

	base := tree{"index.mjs": "one", "nested/dep.js": "two"}

	t.Run("is 16 lowercase hex characters", func(t *testing.T) {
		t.Parallel()

		sum := hashOf(t, base, nil)
		if len(sum) != buildIDLength {
			t.Fatalf("hash = %q, want %d characters", sum, buildIDLength)
		}
		if sum != strings.ToLower(sum) {
			t.Errorf("hash = %q, want lowercase", sum)
		}
		if strings.Trim(sum, "0123456789abcdef") != "" {
			t.Errorf("hash = %q, want hex", sum)
		}
	})

	t.Run("an empty directory still hashes", func(t *testing.T) {
		t.Parallel()

		if sum := hashOf(t, tree{}, nil); len(sum) != buildIDLength {
			t.Errorf("hash = %q, want %d characters", sum, buildIDLength)
		}
	})

	variants := []struct {
		name  string
		files tree
		same  bool
	}{
		{name: "the same tree", files: tree{"index.mjs": "one", "nested/dep.js": "two"}, same: true},
		{name: "different contents", files: tree{"index.mjs": "ONE", "nested/dep.js": "two"}},
		{name: "a different path", files: tree{"index.mjs": "one", "nested/other.js": "two"}},
		{name: "an extra file", files: tree{"index.mjs": "one", "nested/dep.js": "two", "extra.js": ""}},
		{name: "contents swapped between paths", files: tree{"index.mjs": "two", "nested/dep.js": "one"}},
	}
	for _, tt := range variants {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			want := hashOf(t, base, nil)
			got := hashOf(t, tt.files, nil)
			if tt.same && got != want {
				t.Errorf("hash = %q, want %q", got, want)
			}
			if !tt.same && got == want {
				t.Errorf("hash = %q for %s, want it to differ", got, tt.name)
			}
		})
	}

	t.Run("directories and symlinks do not count", func(t *testing.T) {
		t.Parallel()

		want := hashOf(t, base, nil)
		got := hashOf(t, base, func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, "empty", "deeper"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, "index.mjs"), filepath.Join(root, "link.mjs")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
		})
		if got != want {
			t.Errorf("hash = %q, want %q — only regular files count", got, want)
		}
	})
}
