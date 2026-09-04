package workspace_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/workspace"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheWorkspaceAnAppIsAMemberOfIsWhatTheImageIsBuiltFrom(t *testing.T) {
	for _, tt := range []struct {
		name    string
		files   map[string]string
		app     string
		path    string
		manager workspace.Manager
	}{
		{
			name: "a pnpm workspace is found by its own file",
			files: map[string]string{
				"pnpm-workspace.yaml":   "packages:\n  - apps/*\n",
				"pnpm-lock.yaml":        "lockfileVersion: '9.0'\n",
				"package.json":          `{"name":"root"}`,
				"apps/web/package.json": `{"name":"web"}`,
			},
			app:     "apps/web",
			path:    "apps/web",
			manager: workspace.Pnpm,
		},
		{
			name: "npm workspaces are declared in the root package.json",
			files: map[string]string{
				"package.json":          `{"name":"root","workspaces":["apps/*"]}`,
				"package-lock.json":     `{"lockfileVersion":3}`,
				"apps/web/package.json": `{"name":"web"}`,
			},
			app:     "apps/web",
			path:    "apps/web",
			manager: workspace.Npm,
		},
		{
			name: "an npm-shrinkwrap stands in for the lockfile",
			files: map[string]string{
				"package.json":          `{"name":"root","workspaces":["apps/*"]}`,
				"npm-shrinkwrap.json":   `{"lockfileVersion":3}`,
				"apps/web/package.json": `{"name":"web"}`,
			},
			app:     "apps/web",
			path:    "apps/web",
			manager: workspace.Npm,
		},
		{
			name: "a yarn.lock alone is yarn classic",
			files: map[string]string{
				"package.json":          `{"name":"root","workspaces":["apps/*"]}`,
				"yarn.lock":             "# yarn lockfile v1\n",
				"apps/web/package.json": `{"name":"web"}`,
			},
			app:     "apps/web",
			path:    "apps/web",
			manager: workspace.YarnClassic,
		},
		{
			name: "a .yarnrc.yml beside the lock is yarn berry",
			files: map[string]string{
				"package.json":          `{"name":"root","workspaces":["apps/*"]}`,
				"yarn.lock":             "__metadata:\n  version: 8\n",
				".yarnrc.yml":           "nodeLinker: node-modules\n",
				"apps/web/package.json": `{"name":"web"}`,
			},
			app:     "apps/web",
			path:    "apps/web",
			manager: workspace.YarnBerry,
		},
		{
			name: "bun's lockfile names bun",
			files: map[string]string{
				"package.json":          `{"name":"root","workspaces":["apps/*"]}`,
				"bun.lock":              "{}\n",
				"apps/web/package.json": `{"name":"web"}`,
			},
			app:     "apps/web",
			path:    "apps/web",
			manager: workspace.Bun,
		},
		{
			name: "packageManager settles a root carrying two lockfiles",
			files: map[string]string{
				"package.json":          `{"name":"root","workspaces":["apps/*"],"packageManager":"yarn@4.5.0"}`,
				"yarn.lock":             "__metadata:\n  version: 8\n",
				"package-lock.json":     `{"lockfileVersion":3}`,
				"apps/web/package.json": `{"name":"web"}`,
			},
			app:     "apps/web",
			path:    "apps/web",
			manager: workspace.YarnBerry,
		},
		{
			name: "an app nested below a workspace member is still located by its own path",
			files: map[string]string{
				"pnpm-workspace.yaml":         "packages:\n  - apps/**\n",
				"pnpm-lock.yaml":              "lockfileVersion: '9.0'\n",
				"package.json":                `{"name":"root"}`,
				"apps/group/web/package.json": `{"name":"web"}`,
			},
			app:     "apps/group/web",
			path:    "apps/group/web",
			manager: workspace.Pnpm,
		},
		{
			name: "an app in no workspace is its own root",
			files: map[string]string{
				"package.json":      `{"name":"solo"}`,
				"package-lock.json": `{"lockfileVersion":3}`,
			},
			app:     ".",
			path:    ".",
			manager: workspace.Npm,
		},
		{
			name: "an app the workspace does not list is its own root",
			files: map[string]string{
				"pnpm-workspace.yaml":      "packages:\n  - apps/*\n",
				"pnpm-lock.yaml":           "lockfileVersion: '9.0'\n",
				"package.json":             `{"name":"root"}`,
				"aside/thing/package.json": `{"name":"thing"}`,
			},
			app:     "aside/thing",
			path:    ".",
			manager: workspace.Unknown,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, tt.files)

			got, err := workspace.Locate(filepath.Join(dir, filepath.FromSlash(tt.app)))
			if err != nil {
				t.Fatalf("Locate() = %v", err)
			}

			wantRoot := dir
			if tt.path == "." {
				wantRoot = filepath.Join(dir, filepath.FromSlash(tt.app))
			}
			if got.Root != wantRoot {
				t.Errorf("Locate().Root = %q, want %q — the image is built from the directory the install reads its lockfile in", got.Root, wantRoot)
			}
			if got.Path != tt.path {
				t.Errorf("Locate().Path = %q, want %q", got.Path, tt.path)
			}
			if got.Manager != tt.manager {
				t.Errorf("Locate().Manager = %q, want %q", got.Manager, tt.manager)
			}
		})
	}
}

func TestTheSearchStopsAtTheRepositoryItIsRunIn(t *testing.T) {
	outer := t.TempDir()
	write(t, outer, map[string]string{
		"pnpm-workspace.yaml":        "packages:\n  - repo/apps/*\n",
		"pnpm-lock.yaml":             "lockfileVersion: '9.0'\n",
		"package.json":               `{"name":"outer"}`,
		"repo/.git/HEAD":             "ref: refs/heads/main\n",
		"repo/apps/web/package.json": `{"name":"web"}`,
	})

	got, err := workspace.Locate(filepath.Join(outer, "repo", "apps", "web"))
	if err != nil {
		t.Fatalf("Locate() = %v", err)
	}

	if got.Root != filepath.Join(outer, "repo", "apps", "web") {
		t.Errorf("Locate().Root = %q, and the search climbed out of the repository the app is checked out in", got.Root)
	}
}

func TestARepositoryRootThatIsItselfTheWorkspaceIsStillFound(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		".git/HEAD":             "ref: refs/heads/main\n",
		"pnpm-workspace.yaml":   "packages:\n  - apps/*\n",
		"pnpm-lock.yaml":        "lockfileVersion: '9.0'\n",
		"package.json":          `{"name":"root"}`,
		"apps/web/package.json": `{"name":"web"}`,
	})

	got, err := workspace.Locate(filepath.Join(dir, "apps", "web"))
	if err != nil {
		t.Fatalf("Locate() = %v", err)
	}

	if got.Root != dir {
		t.Errorf("Locate().Root = %q, want the repository root %q, which is the workspace", got.Root, dir)
	}
}

func TestAWorkspaceDependencyWithNoLockfileToResolveItIsRefusedWithTheFileToAdd(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"pnpm-workspace.yaml":   "packages:\n  - apps/*\n",
		"package.json":          `{"name":"root"}`,
		"apps/web/package.json": `{"name":"web","dependencies":{"@acme/lib":"workspace:^"}}`,
	})

	_, err := workspace.Locate(filepath.Join(dir, "apps", "web"))
	if err == nil {
		t.Fatal("Locate() accepted an app whose workspace: dependency nothing in the image could resolve")
	}
	for _, want := range []string{"web", "workspace:", "pnpm-lock.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Locate() = %v, and the reader is never told about %q", err, want)
		}
	}
}

func TestTheAppsOwnScriptsAreWhatTheImageRuns(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"pnpm-workspace.yaml":   "packages:\n  - apps/*\n",
		"pnpm-lock.yaml":        "lockfileVersion: '9.0'\n",
		"package.json":          `{"name":"root","scripts":{"build":"turbo build","start":"echo root"}}`,
		"apps/web/package.json": `{"name":"@acme/web","scripts":{"start":"node server.js"}}`,
	})

	got, err := workspace.Locate(filepath.Join(dir, "apps", "web"))
	if err != nil {
		t.Fatalf("Locate() = %v", err)
	}

	if got.App.Name != "@acme/web" {
		t.Errorf("Locate().App.Name = %q, want the app's own package name", got.App.Name)
	}
	if got.App.Build {
		t.Error("Locate() read a build script onto an app that declares none, so the root's build would run in its place")
	}
	if !got.App.Start {
		t.Error("Locate() missed the app's own start script")
	}
}

func TestAGlobTheWorkspaceNegatesLeavesTheAppOutOfIt(t *testing.T) {
	for _, tt := range []struct {
		name     string
		packages string
		member   bool
	}{
		{
			name:     "a negation after the glob that matched excludes the app",
			packages: "  - apps/*\n  - '!apps/legacy'\n",
			member:   false,
		},
		{
			name:     "a negation the next glob matches again is overruled by it",
			packages: "  - '!apps/legacy'\n  - apps/*\n",
			member:   true,
		},
		{
			name:     "a negation naming another member leaves this one in",
			packages: "  - apps/*\n  - '!apps/docs'\n",
			member:   true,
		},
		{
			name:     "a deep negation excludes what the recursive glob swept in",
			packages: "  - apps/**\n  - '!apps/legacy/**'\n",
			member:   false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, map[string]string{
				"pnpm-workspace.yaml":      "packages:\n" + tt.packages,
				"pnpm-lock.yaml":           "lockfileVersion: '9.0'\n",
				"package.json":             `{"name":"root"}`,
				"apps/legacy/package.json": `{"name":"legacy"}`,
			})

			appDir := filepath.Join(dir, "apps", "legacy")
			got, err := workspace.Locate(appDir)
			if err != nil {
				t.Fatalf("Locate() = %v", err)
			}

			if tt.member && got.Root != dir {
				t.Errorf("Locate().Root = %q, want the workspace root %q — the last glob to match the app decides whether it is a member", got.Root, dir)
			}
			if !tt.member && got.Root != appDir {
				t.Errorf("Locate().Root = %q, and the workspace excludes the app, so it is built from its own directory %q", got.Root, appDir)
			}
		})
	}
}
