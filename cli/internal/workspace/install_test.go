package workspace_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/workspace"
)

const declaresDependencies = `{"name":"app","dependencies":{"ocel":"^1.0.0"}}`

func TestEachPackageManagerInstallsFromItsLockfileWithoutRewritingIt(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{
			name:  "npm installs clean from package-lock.json",
			files: map[string]string{"package-lock.json": "{}"},
			want:  []string{"npm", "ci"},
		},
		{
			name:  "npm installs clean from npm-shrinkwrap.json",
			files: map[string]string{"npm-shrinkwrap.json": "{}"},
			want:  []string{"npm", "ci"},
		},
		{
			name:  "pnpm refuses a lockfile its manifest has outgrown",
			files: map[string]string{"pnpm-lock.yaml": "lockfileVersion: '9.0'\n"},
			want:  []string{"pnpm", "install", "--frozen-lockfile"},
		},
		{
			name:  "classic yarn refuses a lockfile its manifest has outgrown",
			files: map[string]string{"yarn.lock": "# yarn lockfile v1\n"},
			want:  []string{"yarn", "install", "--frozen-lockfile"},
		},
		{
			name:  "yarn berry installs immutably",
			files: map[string]string{"yarn.lock": "__metadata:\n  version: 8\n"},
			want:  []string{"yarn", "install", "--immutable"},
		},
		{
			name:  "bun refuses a lockfile its manifest has outgrown",
			files: map[string]string{"bun.lock": "{}"},
			want:  []string{"bun", "install", "--frozen-lockfile"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.files["package.json"] = declaresDependencies
			write(t, dir, tt.files)

			got, missing, err := workspace.FindMissingInstall(filepath.Join(dir, "resources"))
			if err != nil {
				t.Fatalf("FindMissingInstall() = %v", err)
			}
			if !missing {
				t.Fatal("FindMissingInstall() found nothing to install for a package whose dependencies are not installed")
			}
			if got.Dir != dir {
				t.Errorf("Install.Dir = %q, want the package %q", got.Dir, dir)
			}
			if !slices.Equal(got.Argv, tt.want) {
				t.Errorf("Install.Argv = %q, want %q", got.Argv, tt.want)
			}
		})
	}
}

func TestAWorkspaceMemberIsInstalledFromTheWorkspaceRoot(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{
			name: "a pnpm workspace",
			files: map[string]string{
				"pnpm-workspace.yaml": "packages:\n  - apps/*\n",
				"pnpm-lock.yaml":      "lockfileVersion: '9.0'\n",
				"package.json":        `{"name":"root"}`,
			},
			want: []string{"pnpm", "install", "--frozen-lockfile"},
		},
		{
			name: "an npm workspace",
			files: map[string]string{
				"package-lock.json": "{}",
				"package.json":      `{"name":"root","workspaces":["apps/*"]}`,
			},
			want: []string{"npm", "ci"},
		},
		{
			name: "a workspace whose root declares what its members import",
			files: map[string]string{
				"package-lock.json":     "{}",
				"package.json":          `{"name":"root","workspaces":["apps/*"],"devDependencies":{"ocel":"^1.0.0"}}`,
				"apps/web/package.json": `{"name":"web"}`,
			},
			want: []string{"npm", "ci"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if _, ok := tt.files["apps/web/package.json"]; !ok {
				tt.files["apps/web/package.json"] = declaresDependencies
			}
			write(t, dir, tt.files)

			got, missing, err := workspace.FindMissingInstall(filepath.Join(dir, "apps", "web", "resources"))
			if err != nil {
				t.Fatalf("FindMissingInstall() = %v", err)
			}
			if !missing {
				t.Fatal("FindMissingInstall() found nothing to install for a member whose workspace is not installed")
			}
			if got.Dir != dir {
				t.Errorf("Install.Dir = %q, want the workspace root %q, which holds the lockfile", got.Dir, dir)
			}
			if !slices.Equal(got.Argv, tt.want) {
				t.Errorf("Install.Argv = %q, want %q", got.Argv, tt.want)
			}
		})
	}
}

func TestDependenciesAlreadyInstalledAreLeftAsTheyAre(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files map[string]string
		from  string
	}{
		{
			name: "node_modules beside the package",
			files: map[string]string{
				"package.json":       declaresDependencies,
				"package-lock.json":  "{}",
				"node_modules/.keep": "",
				"resources/index.ts": "",
			},
			from: "resources",
		},
		{
			name: "node_modules at the root of the workspace the package is a member of",
			files: map[string]string{
				"pnpm-workspace.yaml":   "packages:\n  - apps/*\n",
				"pnpm-lock.yaml":        "lockfileVersion: '9.0'\n",
				"package.json":          `{"name":"root"}`,
				"node_modules/.keep":    "",
				"apps/web/package.json": declaresDependencies,
			},
			from: "apps/web/resources",
		},
		{
			name: "a yarn plug'n'play install, which writes no node_modules",
			files: map[string]string{
				"package.json": declaresDependencies,
				"yarn.lock":    "__metadata:\n  version: 8\n",
				".pnp.cjs":     "",
			},
			from: "resources",
		},
		{
			name: "node_modules in a directory above the package, where node resolves from too",
			files: map[string]string{
				".git/HEAD":              "ref: refs/heads/main\n",
				"node_modules/.keep":     "",
				"app/package.json":       declaresDependencies,
				"app/package-lock.json":  "{}",
				"app/resources/index.ts": "",
			},
			from: "app/resources",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, tt.files)

			got, missing, err := workspace.FindMissingInstall(filepath.Join(dir, filepath.FromSlash(tt.from)))
			if err != nil {
				t.Fatalf("FindMissingInstall() = %v", err)
			}
			if missing {
				t.Errorf("FindMissingInstall() = %+v, and would install over dependencies already installed", got)
			}
		})
	}
}

func TestADirectoryWithNoDependenciesToInstallNeedsNoInstall(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files map[string]string
		from  string
	}{
		{
			name:  "a package declaring no dependencies",
			files: map[string]string{"package.json": `{"name":"app"}`, "resources/index.ts": ""},
			from:  "resources",
		},
		{
			name:  "no package at all",
			files: map[string]string{".git/HEAD": "ref: refs/heads/main\n", "resources/index.ts": ""},
			from:  "resources",
		},
		{
			name: "a package outside the repository the directory is checked out in",
			files: map[string]string{
				"package.json":            declaresDependencies,
				"package-lock.json":       "{}",
				"repo/.git/HEAD":          "ref: refs/heads/main\n",
				"repo/resources/index.ts": "",
			},
			from: "repo/resources",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, tt.files)

			got, missing, err := workspace.FindMissingInstall(filepath.Join(dir, filepath.FromSlash(tt.from)))
			if err != nil {
				t.Fatalf("FindMissingInstall() = %v", err)
			}
			if missing {
				t.Errorf("FindMissingInstall() = %+v, want nothing to install", got)
			}
		})
	}
}

func TestMissingDependenciesWithNoLockfileAreRefusedWithWhereToInstallThem(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"package.json": declaresDependencies})

	_, _, err := workspace.FindMissingInstall(filepath.Join(dir, "resources"))
	if err == nil {
		t.Fatal("FindMissingInstall() accepted dependencies that no install can reproduce without writing a lockfile")
	}
	for _, want := range []string{dir, "lockfile", "install"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("FindMissingInstall() = %v, and the reader is never told about %q", err, want)
		}
	}
}

func TestAnInstallReadsAsTheCommandToRun(t *testing.T) {
	install := workspace.Install{Dir: "/src", Argv: []string{"pnpm", "install", "--frozen-lockfile"}}

	if got, want := install.Command(), "pnpm install --frozen-lockfile"; got != want {
		t.Errorf("Command() = %q, want %q", got, want)
	}
}
