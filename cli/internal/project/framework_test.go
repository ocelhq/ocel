package project

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/language"
)

func writeFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

const nextManifest = `{"dependencies":{"next":"15.0.0"}}`

const nodeManifest = "package.json"

func TestLoadReadsTheFrameworkOffTheAppItself(t *testing.T) {
	t.Parallel()

	t.Run("an app naming no framework is read from its own directory", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeConfig(t, root, `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: "services/web" }],
};
`)
		writeFile(t, filepath.Join(root, "services", "web", "package.json"), nextManifest)

		cfg, err := Load(context.Background(), root, "")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got := cfg.Apps[0]; got.Framework != "next" || got.Language != language.JS || got.Evidence != "package.json depends on next" {
			t.Fatalf("Apps[0] = %+v, want next detected from its package.json: the config never named it", got)
		}
	})

	t.Run("a named framework overrides what the directory contains", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeConfig(t, root, `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: "services/web", framework: "node", compute: "serverless" }],
};
`)
		writeFile(t, filepath.Join(root, "services", "web", "package.json"), nextManifest)
		writeFile(t, filepath.Join(root, "services", "web", "server.ts"), "export {}\n")

		cfg, err := Load(context.Background(), root, "")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got := cfg.Apps[0]; got.Framework != "node" || got.Language != language.JS || got.FrameworkSource != language.Configured || got.Entrypoint != "server.ts" {
			t.Fatalf("Apps[0] = %+v, want node as the config names it", got)
		}
	})

	t.Run("a serverless app whose directory says nothing refuses the config and names the app", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeConfig(t, root, `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: "services/web", compute: "serverless" }],
};
`)
		writeFile(t, filepath.Join(root, "services", "web", "main.rb"), "puts 1\n")

		_, err := Load(context.Background(), root, "")
		if err == nil {
			t.Fatal("Load = nil error, want the config refused: nothing in the app's directory says what it is")
		}
		for _, want := range []string{`app "web"`, "framework"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, missing %q", err, want)
			}
		}
	})

	t.Run("an app naming no compute whose directory says nothing loads, holding the refusal for when it runs serverless", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeConfig(t, root, `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: "services/web" }],
};
`)
		writeFile(t, filepath.Join(root, "services", "web", "Dockerfile"), "FROM scratch\n")

		cfg, err := Load(context.Background(), root, "")
		if err != nil {
			t.Fatalf("Load: %v — the provider may run web as a container, which needs no framework", err)
		}
		if framework := cfg.Apps[0].Framework; framework != "" {
			t.Fatalf("Apps[0].Framework = %q, want none", framework)
		}
		_, err = cfg.ResolveComputes([]string{"serverless"}, "fake")
		if err == nil {
			t.Fatal("ResolveComputes(serverless) = nil error, want the refusal the undetected framework holds")
		}
		for _, want := range []string{`app "web"`, "framework"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, missing %q", err, want)
			}
		}
	})

	t.Run("an app whose directory is not there resolves to no framework, leaving the path to be refused where it is read", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeConfig(t, root, `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: "services/web" }],
};
`)

		cfg, err := Load(context.Background(), root, "")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got := cfg.Apps[0]; got.Framework != "" || got.Language != "" {
			t.Fatalf("Apps[0] = %+v, want no framework: nothing exists at the path to be read", got)
		}
	})

	t.Run("a container app is read with no framework at all", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeConfig(t, root, `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: "services/web", compute: "container" }],
};
`)
		writeFile(t, filepath.Join(root, "services", "web", "main.rb"), "puts 1\n")

		cfg, err := Load(context.Background(), root, "")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Apps[0].Framework != "" || cfg.Apps[0].Container == nil {
			t.Fatalf("Apps[0] = %+v, want a container shape and no framework: a container runs the image it is given", cfg.Apps[0])
		}
	})

	t.Run("a container app whose directory is a Next app is read as next", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeConfig(t, root, `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: "services/web", compute: "container" }],
};
`)
		writeFile(t, filepath.Join(root, "services", "web", "package.json"), `{"dependencies":{"next":"15.0.0"}}`)

		cfg, err := Load(context.Background(), root, "")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got := cfg.Apps[0]; got.Container == nil || got.Framework != "next" {
			t.Fatalf("Apps[0] = %+v, want a container that keeps next", got)
		}
	})

	t.Run("a container app whose package.json cannot be read loads with no framework", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeConfig(t, root, `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: "services/web", compute: "container" }],
};
`)
		writeFile(t, filepath.Join(root, "services", "web", "package.json"), `{not json`)

		cfg, err := Load(context.Background(), root, "")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got := cfg.Apps[0]; got.Framework != "" || got.Language != language.JS {
			t.Fatalf("Apps[0] = %+v, want no framework but js: no detector matches a package.json that cannot be read, and a package.json is the one manifest there", got)
		}
	})
}

func loadApps(t *testing.T, apps string, files map[string]string) (*Project, error) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"shop","apps":`+apps+`}`)
	for name, body := range files {
		write(t, filepath.Join(dir, filepath.FromSlash(name)), body)
	}
	return Load(context.Background(), dir, "")
}

func TestAnUndetectedAppIsWrittenInTheOneLanguageWhoseManifestItsDirectoryHolds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		files map[string]string
		want  language.Language
	}{
		{"a package.json starting the app with a script", map[string]string{"web/package.json": `{"scripts":{"start":"node dist/main.js"}}`}, language.JS},
		{"a pyproject.toml with no entrypoint", map[string]string{"web/pyproject.toml": "[project]\nname = \"web\"\n"}, language.Python},
		{"a Cargo.toml with no binary", map[string]string{"web/Cargo.toml": "[package]\nname = \"web\"\n", "web/src/lib.rs": "\n"}, language.Rust},
		{"a package.json beside a pyproject.toml", map[string]string{"web/package.json": `{}`, "web/pyproject.toml": "\n"}, ""},
		{"no manifest", map[string]string{"web/Dockerfile": "FROM scratch\n"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := loadApps(t, `[{"name":"web","path":"web","compute":"container"}]`, tc.files)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.Apps[0]; got.Framework != "" || got.Language != tc.want {
				t.Errorf("Apps[0] = %+v, want no framework and language %q", got, tc.want)
			}
		})
	}
}

func TestAnUndetectedGoAppCarriesTheModuleItSitsIn(t *testing.T) {
	t.Parallel()

	cfg, err := loadApps(t, `[{"name":"api","path":"services/api","compute":"container"}]`, map[string]string{"go.mod": "module example.com/shop\n", "services/api/lib.go": "package api\n"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Apps[0]; got.Language != language.Go || got.GoModule == nil || got.GoModule.Dir != cfg.Dir {
		t.Errorf("Apps[0] = %+v, want go in the module at %s", got, cfg.Dir)
	}
}

func TestAContainerAppStartedByAPackageScriptIsAJSApp(t *testing.T) {
	t.Parallel()

	cfg, err := loadApps(t, `[{"name":"web","path":"web","compute":"container"}]`, map[string]string{"web/package.json": `{"scripts":{"start":"node dist/main.js"}}`})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.HasJSApp() {
		t.Error("HasJSApp() = false, want true: the app's one manifest is a package.json")
	}
}

func TestANamedNodeAppWithNoEntrypointIsRefusedServerless(t *testing.T) {
	t.Parallel()

	files := map[string]string{"web/package.json": `{"scripts":{"start":"node dist/main.js"}}`}
	want := `app "web" is built with node, and web holds a package.json with no entrypoint among src/server`

	_, err := loadApps(t, `[{"name":"web","path":"web","framework":"node","compute":"serverless"}]`, files)
	if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), `set "entrypoint"`) {
		t.Errorf("Load err = %v, want it to contain %q and say to set \"entrypoint\"", err, want)
	}

	cfg, err := loadApps(t, `[{"name":"web","path":"web","framework":"node"}]`, files)
	if err != nil {
		t.Fatalf("Load: %v — the provider may run web as a container, which starts from its start script", err)
	}
	if _, err := cfg.ResolveComputes([]string{"serverless"}, "fake"); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("ResolveComputes(serverless) err = %v, want it to contain %q", err, want)
	}
	if _, err := cfg.ResolveComputes([]string{"container"}, "fake"); err != nil {
		t.Errorf("ResolveComputes(container) = %v, want web run in a container", err)
	}
}

func TestANamedPythonAppWithNoEntrypointIsRefusedServerless(t *testing.T) {
	t.Parallel()

	files := map[string]string{"api/pyproject.toml": "[project]\nname = \"api\"\n", "api/Dockerfile": "FROM python:3.13\n"}
	want := `app "api" is built with python, and api holds a pyproject.toml with no app.py, index.py, server.py, main.py, wsgi.py or asgi.py at its root, in src/ or in app/: set "entrypoint"`

	_, err := loadApps(t, `[{"name":"api","path":"api","framework":"python","compute":"serverless"}]`, files)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Load err = %v, want it to contain %q", err, want)
	}

	cfg, err := loadApps(t, `[{"name":"api","path":"api","framework":"python"}]`, files)
	if err != nil {
		t.Fatalf("Load: %v — the provider may run api as a container, which its Dockerfile starts", err)
	}
	if _, err := cfg.ResolveComputes([]string{"serverless"}, "fake"); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("ResolveComputes(serverless) err = %v, want it to contain %q", err, want)
	}
	if _, err := cfg.ResolveComputes([]string{"container"}, "fake"); err != nil {
		t.Errorf("ResolveComputes(container) = %v, want api run in a container", err)
	}

	cfg, err = loadApps(t, `[{"name":"api","path":"api","framework":"python","entrypoint":"service.py"}]`, map[string]string{"api/pyproject.toml": "\n", "api/service.py": "\n"})
	if err != nil || cfg.Apps[0].Entrypoint != "service.py" {
		t.Errorf("Load = %v, want the declared entrypoint served", err)
	}
}

func TestANamedGoAppInNoModuleIsRefusedServerless(t *testing.T) {
	t.Parallel()

	cfg, err := loadApps(t, `[{"name":"api","path":"api","framework":"go"}]`, map[string]string{"api/main.go": "package main\n\nfunc main() {}\n"})
	if err != nil {
		t.Fatalf("Load: %v — a container may build the app from its own Dockerfile", err)
	}
	if _, err := cfg.ResolveComputes([]string{"serverless"}, "fake"); err == nil || !strings.Contains(err.Error(), "go.mod") {
		t.Errorf("ResolveComputes(serverless) err = %v, want it to say no go.mod holds the app", err)
	}
}

func TestAGoAppsPackageIsResolvedFromItsModuleAtLoad(t *testing.T) {
	t.Parallel()

	files := map[string]string{"go.mod": "module example.com/shop\n", "services/api/cmd/api/main.go": "package main\n\nfunc main() {}\n"}
	cfg, err := loadApps(t, `[{"name":"api","path":"services/api"}]`, files)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Apps[0]; got.GoModule == nil || got.GoModule.Dir != cfg.Dir || got.GoPackage != "./services/api/cmd/api" {
		t.Errorf("Apps[0] = %+v, want ./services/api/cmd/api in the module at %s", got, cfg.Dir)
	}

	_, err = loadApps(t, `[{"name":"api","path":"services/api","entrypoint":"cmd/api/main.go"}]`, files)
	if err == nil || !strings.Contains(err.Error(), `"cmd/api/main.go"`) {
		t.Errorf("Load err = %v, want the entrypoint naming a file refused", err)
	}
}

func TestDescribeFrameworksSaysWhereEachAppsFrameworkCameFrom(t *testing.T) {
	t.Parallel()

	cfg, err := loadApps(t, `[{"name":"web","path":"web"},{"name":"api","path":"api","framework":"node"},{"name":"box","path":"box"}]`, map[string]string{
		"web/package.json": `{"dependencies":{"next":"15"}}`,
		"api/package.json": `{}`,
		"api/index.ts":     "\n",
		"box/Dockerfile":   "FROM scratch\n",
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"web: next (package.json depends on next)", "api: node (set in ocel.json)", "box: no framework detected"}
	if got := cfg.DescribeFrameworks(); !reflect.DeepEqual(got, want) {
		t.Errorf("DescribeFrameworks() = %q, want %q", got, want)
	}
}
