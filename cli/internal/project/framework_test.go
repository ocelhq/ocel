package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func appDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return dir
}

func TestDetectFramework(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name:  "a package.json depending on next is a next app",
			files: map[string]string{"package.json": `{"dependencies":{"next":"15.0.0"}}`},
			want:  "next",
		},
		{
			name:  "next in devDependencies names it too",
			files: map[string]string{"package.json": `{"devDependencies":{"next":"15.0.0"}}`},
			want:  "next",
		},
		{
			name:  "a next config names it where the manifest does not",
			files: map[string]string{"package.json": `{}`, "next.config.ts": "export default {};"},
			want:  "next",
		},
		{
			name:  "a package.json alone is a node app",
			files: map[string]string{"package.json": `{"name":"api"}`},
			want:  "node",
		},
		{
			name:  "a go module is a go app",
			files: map[string]string{"go.mod": "module example.com/api\n"},
			want:  "go",
		},
		{
			name:  "a pyproject is a python app",
			files: map[string]string{"pyproject.toml": "[project]\nname = \"api\"\n"},
			want:  "python",
		},
		{
			name:  "requirements.txt names a python app too",
			files: map[string]string{"requirements.txt": "flask\n"},
			want:  "python",
		},
		{
			name:  "a cargo manifest is a rust app",
			files: map[string]string{"Cargo.toml": "[package]\nname = \"api\"\n"},
			want:  "rust",
		},
		{
			name:  "a cargo manifest beside a package.json is the native addon of a node app",
			files: map[string]string{"package.json": `{"name":"api"}`, "Cargo.toml": "[package]\nname = \"api\"\n"},
			want:  "node",
		},
		{
			name:  "a cargo manifest beside a next manifest is still next",
			files: map[string]string{"package.json": nextManifest, "Cargo.toml": "[package]\nname = \"api\"\n"},
			want:  "next",
		},
		{
			name:  "a cargo manifest beside a pyproject is the extension module of a python app",
			files: map[string]string{"pyproject.toml": "[project]\nname = \"api\"\n", "Cargo.toml": "[package]\nname = \"api\"\n"},
			want:  "python",
		},
		{
			name:  "a package.json beside a go module lists the go app's tooling",
			files: map[string]string{"package.json": `{}`, "go.mod": "module example.com/api\n"},
			want:  "go",
		},
		{
			name:  "a next manifest beside a go module is still the go app's tooling",
			files: map[string]string{"package.json": nextManifest, "go.mod": "module example.com/api\n"},
			want:  "go",
		},
		{
			name:  "a go module comes before a cargo manifest beside it",
			files: map[string]string{"Cargo.toml": "[package]\nname = \"api\"\n", "go.mod": "module example.com/api\n"},
			want:  "go",
		},
		{
			name:  "a next config decides it before the manifest is ever read",
			files: map[string]string{"package.json": "{ not json", "next.config.ts": "export default {};"},
			want:  "next",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := detectFramework(appDir(t, tc.files))
			if err != nil {
				t.Fatalf("detectFramework = %v", err)
			}
			if got != tc.want {
				t.Errorf("detectFramework = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDetectFrameworkRefusals(t *testing.T) {
	t.Parallel()

	t.Run("a directory containing no manifest names the directory and the key that decides it", func(t *testing.T) {
		t.Parallel()

		dir := appDir(t, map[string]string{"main.rb": "puts 1\n"})

		_, err := detectFramework(dir)
		if err == nil {
			t.Fatal("detectFramework = nil error, want a refusal: nothing here says what the app is")
		}
		for _, want := range []string{dir, "framework"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, missing %q", err, want)
			}
		}
	})

	t.Run("a package.json that is not JSON is surfaced rather than read as plain node", func(t *testing.T) {
		t.Parallel()

		dir := appDir(t, map[string]string{"package.json": "{ not json"})

		_, err := detectFramework(dir)
		if err == nil {
			t.Fatal("detectFramework = nil error, want a refusal: the manifest cannot be read")
		}
		if !strings.Contains(err.Error(), nodeManifest) {
			t.Errorf("error = %q, missing %q", err, nodeManifest)
		}
	})
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
		if got, want := cfg.Apps[0].Serverless, (&Serverless{Framework: "next", Detected: true}); *got != *want {
			t.Fatalf("Apps[0].Serverless = %+v, want %+v: the app's own manifest says what it is, and the config never named it", got, want)
		}
	})

	t.Run("a named framework overrides what the directory contains", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeConfig(t, root, `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: "services/web", framework: "node" }],
};
`)
		writeFile(t, filepath.Join(root, "services", "web", "package.json"), nextManifest)

		cfg, err := Load(context.Background(), root, "")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got, want := cfg.Apps[0].Serverless, (&Serverless{Framework: "node"}); *got != *want {
			t.Fatalf("Apps[0].Serverless = %+v, want %+v: a named framework decides it", got, want)
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
		if framework := cfg.Apps[0].Framework(); framework != "" {
			t.Fatalf("Apps[0].Framework() = %q, want none", framework)
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
		if cfg.Apps[0].Serverless != nil {
			t.Fatalf("Apps[0].Serverless = %+v, want none: nothing exists at the path to be read", cfg.Apps[0].Serverless)
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
		if cfg.Apps[0].Serverless != nil || cfg.Apps[0].Container == nil {
			t.Fatalf("Apps[0] = %+v, want a container shape and no framework: a container runs the image it is given", cfg.Apps[0])
		}
	})
}
