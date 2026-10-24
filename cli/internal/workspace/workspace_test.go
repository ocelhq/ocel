package workspace_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/gomodule"
	"github.com/ocelhq/ocel/cli/internal/language"
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
		manager workspace.PackageManager
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
			name: "an npm-shrinkwrap counts as the lockfile",
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
			name: "packageManager picks the manager for a root containing two lockfiles",
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

			got, err := workspace.Locate(filepath.Join(dir, filepath.FromSlash(tt.app)), workspace.Program{Language: language.JS})
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
			if got.PackageManager != tt.manager {
				t.Errorf("Locate().PackageManager = %q, want %q", got.PackageManager, tt.manager)
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

	got, err := locate(t, outer, filepath.Join(outer, "repo", "apps", "web"), language.JS)
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

	got, err := locate(t, dir, filepath.Join(dir, "apps", "web"), language.JS)
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

	_, err := locate(t, dir, filepath.Join(dir, "apps", "web"), language.JS)
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

	got, err := locate(t, dir, filepath.Join(dir, "apps", "web"), language.JS)
	if err != nil {
		t.Fatalf("Locate() = %v", err)
	}

	if got.Package.Name != "@acme/web" {
		t.Errorf("Locate().Package.Name = %q, want the app's own package name", got.Package.Name)
	}
	if got.Package.Build {
		t.Error("Locate() read a build script onto an app that declares none, so the root's build would run in its place")
	}
	if !got.Package.Start {
		t.Error("Locate() missed the app's own start script")
	}
}

func TestTheMembersOfAWorkspaceAreTheNamesItsPackagesGoBy(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"pnpm-workspace.yaml":                        "packages:\n  - apps/*\n  - packages/*\n  - '!packages/legacy'\n",
		"pnpm-lock.yaml":                             "lockfileVersion: '9.0'\n",
		"package.json":                               `{"name":"root"}`,
		"apps/web/package.json":                      `{"name":"@acme/web"}`,
		"packages/lib/package.json":                  `{"name":"@acme/lib"}`,
		"packages/legacy/package.json":               `{"name":"@acme/legacy"}`,
		"packages/lib/node_modules/dep/package.json": `{"name":"dep"}`,
	})

	members := located(t, filepath.Join(dir, "apps", "web"), language.JS).Members()

	want := []string{"@acme/lib", "@acme/web"}
	if len(members) != len(want) {
		t.Fatalf("Members() = %v, want %v — an installed dependency and a package the workspace excludes are not members of it", members, want)
	}
	for i, name := range want {
		if members[i] != name {
			t.Errorf("Members() = %v, want %v", members, want)
		}
	}
}

func TestAnAppIsBuiltFromTheNodeWorkspaceItSitsInUnlessItIsWrittenInAnotherLanguage(t *testing.T) {
	for _, written := range []language.Language{language.JS, language.Python, language.Rust, ""} {
		t.Run("language "+string(written), func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, map[string]string{
				"pnpm-workspace.yaml":     "packages:\n  - apps/*\n",
				"pnpm-lock.yaml":          "lockfileVersion: '9.0'\n",
				"package.json":            `{"name":"root"}`,
				"apps/svc/package.json":   `{"name":"@acme/svc"}`,
				"apps/svc/pyproject.toml": "[project]\nname = \"svc\"\n",
				"apps/svc/main.py":        "print('hi')\n",
			})
			app := filepath.Join(dir, "apps", "svc")

			loc := located(t, app, written)

			if loc.Language != written {
				t.Errorf("Language = %q, want %q, the language the app is written in", loc.Language, written)
			}
			wantRoot, wantMember := app, false
			if written == language.JS || written == "" {
				wantRoot, wantMember = dir, true
			}
			if loc.Root != wantRoot {
				t.Errorf("Root = %s, want %s — an app is built from the workspace it installs with unless it is written in a language other than JS", loc.Root, wantRoot)
			}
			if loc.Member != wantMember {
				t.Errorf("Member = %v, want %v", loc.Member, wantMember)
			}
		})
	}
}

func TestAnAppInNoWorkspaceHasNoMembersToTellApartFromTheRegistry(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"package.json":      `{"name":"solo"}`,
		"package-lock.json": `{"lockfileVersion":3}`,
	})

	if members := located(t, dir, language.JS).Members(); len(members) != 0 {
		t.Errorf("Members() = %v, want none for an app that is its own root", members)
	}
}

func TestABuildContextIsTakenOnlyWhereTheInstallStillHasEverythingItReads(t *testing.T) {
	workspaceFiles := map[string]string{
		"repo/pnpm-workspace.yaml":   "packages:\n  - apps/*\n",
		"repo/pnpm-lock.yaml":        "lockfileVersion: '9.0'\n",
		"repo/package.json":          `{"name":"root"}`,
		"repo/apps/web/package.json": `{"name":"web","scripts":{"start":"node server.js"}}`,
	}

	t.Run("the workspace root the app was located in is taken", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, workspaceFiles)

		rebased, err := located(t, filepath.Join(dir, "repo", "apps", "web"), language.JS).Rebase(filepath.Join(dir, "repo"))
		if err != nil {
			t.Fatalf("Rebase() = %v", err)
		}
		if !rebased.Member || rebased.Path != "apps/web" || rebased.PackageManager != workspace.Pnpm {
			t.Errorf("Rebase() = %+v, want the app still a member of the workspace it was located in", rebased)
		}
	})

	t.Run("a directory above the workspace root is taken, and the app is no longer scoped as a member", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, workspaceFiles)

		rebased, err := located(t, filepath.Join(dir, "repo", "apps", "web"), language.JS).Rebase(dir)
		if err != nil {
			t.Fatalf("Rebase() = %v", err)
		}
		if rebased.Root != dir || rebased.Path != "repo/apps/web" {
			t.Errorf("Rebase() = %+v, want the app addressed from the context it was pointed at", rebased)
		}
		if rebased.Member {
			t.Errorf("Rebase() left the app a member of a workspace %s declares nothing about, so the install would be filtered against a workspace that is not there", dir)
		}
		commands, err := rebased.Commands()
		if err != nil {
			t.Fatalf("Commands() = %v", err)
		}
		if !reflect.DeepEqual(commands, workspace.Commands{}) {
			t.Errorf("Commands() = %+v, want railpack left to plan a context that declares no workspace", commands)
		}
	})

	t.Run("a directory containing no lockfile for the app's workspace: dependency is refused", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, map[string]string{
			"repo/pnpm-workspace.yaml":   "packages:\n  - apps/*\n",
			"repo/pnpm-lock.yaml":        "lockfileVersion: '9.0'\n",
			"repo/package.json":          `{"name":"root"}`,
			"repo/apps/web/package.json": `{"name":"web","dependencies":{"@acme/lib":"workspace:^"}}`,
		})

		_, err := located(t, filepath.Join(dir, "repo", "apps", "web"), language.JS).Rebase(dir)
		if err == nil {
			t.Fatal("Rebase() took a context containing no lockfile for the app's workspace: dependency, so the install inside the image resolves nothing")
		}
		if !strings.Contains(err.Error(), "workspace:") || !strings.Contains(err.Error(), pnpmLockName) {
			t.Errorf("Rebase() = %v, and the reader is never told what the context is missing", err)
		}
	})

	t.Run("a directory the app does not sit under is refused by what image.context may name", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, workspaceFiles)
		elsewhere := filepath.Join(dir, "elsewhere")
		if err := os.MkdirAll(elsewhere, 0o755); err != nil {
			t.Fatal(err)
		}

		_, err := located(t, filepath.Join(dir, "repo", "apps", "web"), language.JS).Rebase(elsewhere)
		if err == nil {
			t.Fatal("Rebase() took a context the app is not inside, so the image would be built without the app in it")
		}
		if !strings.Contains(err.Error(), "image.context") {
			t.Errorf("Rebase() = %v, and the reader is never told what image.context may point at", err)
		}
	})

	t.Run("a directory inside the workspace but above nothing the app needs is refused", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, workspaceFiles)

		_, err := located(t, filepath.Join(dir, "repo", "apps", "web"), language.JS).Rebase(filepath.Join(dir, "repo", "apps"))
		if err == nil {
			t.Fatal("Rebase() took a context below the workspace root, so the install would read a lockfile the context does not contain")
		}
	})
}

const pnpmLockName = "pnpm-lock.yaml"

func locate(t *testing.T, project, dir string, written language.Language) (workspace.Location, error) {
	t.Helper()
	return locateWith(t, project, dir, written, "")
}

func locateWith(t *testing.T, project, dir string, written language.Language, entrypoint string) (workspace.Location, error) {
	t.Helper()
	program := workspace.Program{Language: written, Entrypoint: entrypoint}
	if written == language.Go {
		module, found, err := gomodule.FindInProject(dir, project)
		if err != nil {
			t.Fatalf("FindInProject(%s, %s) = %v", dir, project, err)
		}
		if found {
			pkg, err := gomodule.PackagePath(module.Dir, dir, entrypoint)
			if err != nil {
				t.Fatalf("PackagePath(%s, %q) = %v", dir, entrypoint, err)
			}
			program.GoModule, program.GoPackage = &module, pkg
		}
	}
	return workspace.Locate(dir, program)
}

func located(t *testing.T, dir string, written language.Language) workspace.Location {
	t.Helper()
	return locatedIn(t, dir, dir, written)
}

func locatedIn(t *testing.T, project, dir string, written language.Language) workspace.Location {
	t.Helper()
	return locatedWith(t, project, dir, written, "")
}

func locatedWith(t *testing.T, project, dir string, written language.Language, entrypoint string) workspace.Location {
	t.Helper()
	loc, err := locateWith(t, project, dir, written, entrypoint)
	if err != nil {
		t.Fatalf("Locate(%s, %s) = %v", project, dir, err)
	}
	return loc
}

func TestYarnBerryIsToldFromClassicByEverythingThatNamesIt(t *testing.T) {
	for _, tt := range []struct {
		name     string
		declared string
		lock     string
		yarnrc   bool
		want     workspace.PackageManager
	}{
		{
			name:   "the .yarnrc.yml beside the lockfile names it",
			lock:   "# yarn lockfile v1\n",
			yarnrc: true,
			want:   workspace.YarnBerry,
		},
		{
			name:     "packageManager names it where nothing else does",
			declared: "yarn@4.5.0",
			lock:     "# yarn lockfile v1\n",
			want:     workspace.YarnBerry,
		},
		{
			name: "the lockfile's own header names it",
			lock: "__metadata:\n  version: 8\n",
			want: workspace.YarnBerry,
		},
		{
			name:     "a yarn 1 lockfile under a yarn 1 packageManager stays classic",
			declared: "yarn@1.22.22",
			lock:     "# yarn lockfile v1\n",
			want:     workspace.YarnClassic,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			root := `{"name":"root","workspaces":["apps/*"]}`
			if tt.declared != "" {
				root = `{"name":"root","packageManager":"` + tt.declared + `","workspaces":["apps/*"]}`
			}
			files := map[string]string{
				"package.json":          root,
				"apps/web/package.json": `{"name":"web"}`,
				"yarn.lock":             tt.lock,
			}
			if tt.yarnrc {
				files[".yarnrc.yml"] = "nodeLinker: node-modules\n"
			}
			write(t, dir, files)

			got, err := locate(t, dir, filepath.Join(dir, "apps", "web"), language.JS)
			if err != nil {
				t.Fatalf("Locate() = %v", err)
			}
			if got.PackageManager != tt.want {
				t.Errorf("Locate().PackageManager = %q, want %q — a berry install is scoped by a command classic yarn does not have", got.PackageManager, tt.want)
			}
		})
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
			got, err := locate(t, dir, appDir, language.JS)
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

func TestAGoAppIsBuiltFromTheModuleItsPackageBelongsTo(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		".git/HEAD":      "ref: refs/heads/main\n",
		"go.mod":         "module example.com/web\n\ngo 1.27.0\n",
		"lib/lib.go":     "package lib\n",
		"server/main.go": "package main\n\nfunc main() {}\n",
	})

	got := locatedIn(t, dir, filepath.Join(dir, "server"), language.Go)

	if got.Root != dir || got.Path != "server" || got.ModuleDir != "." || got.Language != language.Go {
		t.Errorf("Locate() = %+v, want the app at server inside the Go module rooted at %s — go build in the image reads the go.mod above the package", got, dir)
	}
}

func TestAGoAppIsBuiltFromTheDirectoryHoldingItsModuleAndEveryModuleItReplacesLocally(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		".git/HEAD":  "ref: refs/heads/main\n",
		"sdk/go.mod": "module ocel.dev\n\ngo 1.27.0\n",
		"tests/fixtures/web/go.mod": "module example.com/web\n\ngo 1.27.0\n\n" +
			"require ocel.dev v0.0.0\n\n" +
			"replace ocel.dev => ../../../sdk\n\n" +
			"replace example.com/pinned => example.com/fork v1.2.3\n",
		"tests/fixtures/web/server/main.go": "package main\n\nfunc main() {}\n",
	})

	got := locatedIn(t, dir, filepath.Join(dir, "tests", "fixtures", "web", "server"), language.Go)

	if got.Root != dir || got.Path != "tests/fixtures/web/server" || got.ModuleDir != "tests/fixtures/web" {
		t.Errorf("Locate() = %+v, want the context %s, the nearest directory holding both the module and the sdk it replaces ocel.dev with", got, dir)
	}
	if got.GoVersion != "1.27.0" {
		t.Errorf("Locate().GoVersion = %q, want the go directive of the app's own module", got.GoVersion)
	}
}

func TestAGoAppBuildsItsOwnPackageWithTheModuleItBelongsTo(t *testing.T) {
	module := map[string]string{
		".git/HEAD":  "ref: refs/heads/main\n",
		"sdk/go.mod": "module ocel.dev\n\ngo 1.27.0\n",
		"apps/web/go.mod": "module example.com/web\n\ngo 1.27.0\n\n" +
			"require ocel.dev v0.0.0\n\nreplace ocel.dev => ../../sdk\n",
		"apps/web/server/main.go":         "package main\n\nfunc main() {}\n",
		"apps/web/server/cmd/api/main.go": "package main\n\nfunc main() {}\n",
	}

	for _, tt := range []struct {
		name       string
		entrypoint string
		build      string
	}{
		{"its own directory", ".", `cd apps/web && GOWORK=off CGO_ENABLED=0 go build -ldflags="-w -s" -o ../../out ./server`},
		{"the directory its entrypoint names", "cmd/api", `cd apps/web && GOWORK=off CGO_ENABLED=0 go build -ldflags="-w -s" -o ../../out ./server/cmd/api`},
	} {
		t.Run("the package in "+tt.name+" is built from its module into the binary railpack starts", func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, module)

			commands, err := locatedWith(t, dir, filepath.Join(dir, "apps", "web", "server"), language.Go, tt.entrypoint).Commands()
			if err != nil {
				t.Fatalf("Commands() = %v", err)
			}
			if !reflect.DeepEqual(commands, workspace.Commands{Build: tt.build}) {
				t.Errorf("Commands() = %+v, want only the build %q — railpack's own go build runs at the context root, which holds no go.mod", commands, tt.build)
			}
		})
	}

	t.Run("a go.work at the context root downloads only what the app's module requires", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, module)
		write(t, dir, map[string]string{"go.work": "go 1.27.0\n\nuse (\n\t./sdk\n\t./tools\n)\n"})

		commands, err := locatedWith(t, dir, filepath.Join(dir, "apps", "web", "server"), language.Go, ".").Commands()
		if err != nil {
			t.Fatalf("Commands() = %+v", err)
		}
		if want := "sh -c 'cd apps/web && GOWORK=off go mod download'"; commands.Install != want {
			t.Errorf("Commands().Install = %q, want %q — railpack downloads every module the go.work uses, and the serverless build compiles with GOWORK=off", commands.Install, want)
		}
	})

	for _, tt := range []struct {
		entrypoint string
		build      string
	}{
		{".", `GOWORK=off CGO_ENABLED=0 go build -ldflags="-w -s" -o out .`},
		{"cmd/api", `GOWORK=off CGO_ENABLED=0 go build -ldflags="-w -s" -o out ./cmd/api`},
	} {
		t.Run("a module rooted in the app's own directory builds the package in "+tt.entrypoint+" rather than the one railpack picks", func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, map[string]string{
				".git/HEAD":           "ref: refs/heads/main\n",
				"go.mod":              "module example.com/web\n\ngo 1.27.0\n",
				"main.go":             "package main\n\nfunc main() {}\n",
				"cmd/api/main.go":     "package main\n\nfunc main() {}\n",
				"cmd/another/main.go": "package main\n\nfunc main() {}\n",
			})

			commands, err := locatedWith(t, dir, dir, language.Go, tt.entrypoint).Commands()
			if err != nil {
				t.Fatalf("Commands() = %v", err)
			}
			if !reflect.DeepEqual(commands, workspace.Commands{Build: tt.build}) {
				t.Errorf("Commands() = %+v, want only the build %q", commands, tt.build)
			}
		})
	}
}

func TestAGoAppInNoModuleIsBuiltFromItsOwnDirectory(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		".git/HEAD":           "ref: refs/heads/main\n",
		"go.mod":              "module example.com/monorepo\n\ngo 1.27.0\n",
		"deploy/ocel.json":    `{"slug":"shop"}`,
		"deploy/api/main.go":  "package main\n\nfunc main() {}\n",
		"project/go.mod":      "module example.com/shop\n\ngo 1.27.0\n",
		"project/api/main.go": "package main\n\nfunc main() {}\n",
	})

	project, app := filepath.Join(dir, "deploy"), filepath.Join(dir, "deploy", "api")
	if got := locatedIn(t, project, app, language.Go); got.ModuleDir != "" || got.Root != app {
		t.Errorf("Locate() = %+v, want the app built from its own directory with no module — the image looks for the module where the function build does", got)
	}
}

func TestAGoAppsBuildQuotesEachDirectoryItNames(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		".git/HEAD":                 "ref: refs/heads/main\n",
		"go.work":                   "go 1.27.0\n",
		"sdk/go.mod":                "module ocel.dev\n\ngo 1.27.0\n",
		"my web/go.mod":             "module example.com/web\n\ngo 1.27.0\n\nrequire ocel.dev v0.0.0\n\nreplace ocel.dev => ../sdk\n",
		"my web/cmd/my api/main.go": "package main\n\nfunc main() {}\n",
		"my web/cmd/it's/main.go":   "package main\n\nfunc main() {}\n",
	})

	for _, tt := range []struct {
		entrypoint string
		build      string
	}{
		{"cmd/my api", `cd 'my web' && GOWORK=off CGO_ENABLED=0 go build -ldflags="-w -s" -o ../out './cmd/my api'`},
		{"cmd/it's", `cd 'my web' && GOWORK=off CGO_ENABLED=0 go build -ldflags="-w -s" -o ../out './cmd/it'\''s'`},
	} {
		commands, err := locatedWith(t, dir, filepath.Join(dir, "my web"), language.Go, tt.entrypoint).Commands()
		if err != nil {
			t.Fatalf("Commands(%q) = %v", tt.entrypoint, err)
		}
		if commands.Build != tt.build {
			t.Errorf("Commands(%q).Build = %s, want %s", tt.entrypoint, commands.Build, tt.build)
		}
		if want := `sh -c 'cd '\''my web'\'' && GOWORK=off go mod download'`; commands.Install != want {
			t.Errorf("Commands(%q).Install = %s, want %s", tt.entrypoint, commands.Install, want)
		}
	}
}

func TestARustAppBuildsAndStartsTheBinaryItsEntrypointNames(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"Cargo.toml":    "[package]\nname = \"server\"\nversion = \"0.1.0\"\n",
		"src/main.rs":   "fn main() {}\n",
		"src/worker.rs": "fn main() {}\n",
	})
	loc := locatedWith(t, dir, dir, language.Rust, "worker")

	commands, err := loc.Commands()
	if err != nil {
		t.Fatalf("Commands() = %v", err)
	}
	want := workspace.Commands{
		Build: "mkdir -p bin && cargo build --release --bin worker --target-dir target && cp target/release/worker bin/",
		Start: "./bin/worker",
	}
	if !reflect.DeepEqual(commands, want) {
		t.Errorf("Commands() = %+v, want %+v — the image builds and runs the binary the function would", commands, want)
	}
}

func TestARustWorkspaceMemberPointedAtItsWorkspaceBuildsTheBinaryWhereItCopiesItFrom(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"Cargo.toml":             "[workspace]\nmembers = [\"crates/api\"]\n",
		"crates/api/Cargo.toml":  "[package]\nname = \"api\"\nversion = \"0.1.0\"\n",
		"crates/api/src/main.rs": "fn main() {}\n",
	})

	app := filepath.Join(dir, "crates", "api")
	rebased, err := locatedWith(t, app, app, language.Rust, "api").Rebase(dir)
	if err != nil {
		t.Fatalf("Rebase() = %v", err)
	}
	commands, err := rebased.Commands()
	if err != nil {
		t.Fatalf("Commands() = %v", err)
	}
	want := workspace.Commands{
		Build:           "cd crates/api && mkdir -p bin && cargo build --release --bin api --target-dir ../../target && cp ../../target/release/api bin/",
		Start:           "./crates/api/bin/api",
		InstallsInBuild: true,
	}
	if !reflect.DeepEqual(commands, want) {
		t.Errorf("Commands() = %+v, want %+v — a member's cargo build writes to the workspace's target unless told where", commands, want)
	}
}

func TestARustAppInADirectoryWithASpaceBuildsAndStartsItsBinaryThere(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"Cargo.toml":              "[workspace]\nmembers = [\"my api\"]\n",
		"my api/Cargo.toml":       "[package]\nname = \"api\"\nversion = \"0.1.0\"\n",
		"my api/src/bin/api-é.rs": "fn main() {}\n",
	})

	app := filepath.Join(dir, "my api")
	rebased, err := locatedWith(t, app, app, language.Rust, "api-é").Rebase(dir)
	if err != nil {
		t.Fatalf("Rebase() = %v", err)
	}
	commands, err := rebased.Commands()
	if err != nil {
		t.Fatalf("Commands() = %v", err)
	}
	want := workspace.Commands{
		Build:           "cd 'my api' && mkdir -p bin && cargo build --release --bin 'api-é' --target-dir ../target && cp '../target/release/api-é' bin/",
		Start:           "'./my api/bin/api-é'",
		InstallsInBuild: true,
	}
	if !reflect.DeepEqual(commands, want) {
		t.Errorf("Commands() = %+v, want %+v", commands, want)
	}
}

func TestARustAppNamingNoEntrypointIsLeftToRailpack(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"Cargo.toml": "[package]\nname = \"server\"\nversion = \"0.1.0\"\n", "src/main.rs": "fn main() {}\n"})

	commands, err := located(t, dir, language.Rust).Commands()
	if err != nil || !reflect.DeepEqual(commands, workspace.Commands{}) {
		t.Errorf("Commands() = %+v, %v, want nothing: railpack builds the crate's binaries and starts the one it runs by default", commands, err)
	}
}

func TestAGoAppAtItsModuleRootNamingNoEntrypointIsLeftToRailpackAndOneBelowItBuildsItsOwnPackage(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		".git/HEAD":          "ref: refs/heads/main\n",
		"go.mod":             "module example.com/web\n\ngo 1.27.0\n",
		"cmd/worker/main.go": "package main\n\nfunc main() {}\n",
		"api/main.go":        "package main\n\nfunc main() {}\n",
	})

	commands, err := located(t, dir, language.Go).Commands()
	if err != nil || !reflect.DeepEqual(commands, workspace.Commands{}) {
		t.Errorf("Commands() = %+v, %v, want nothing: railpack builds cmd/worker, the first command it finds", commands, err)
	}

	commands, err = locatedIn(t, dir, filepath.Join(dir, "api"), language.Go).Commands()
	if want := `GOWORK=off CGO_ENABLED=0 go build -ldflags="-w -s" -o out ./api`; err != nil || commands.Build != want {
		t.Errorf("Commands() = %+v, %v, want the build %q: railpack would build the module root, not the app", commands, err, want)
	}
}

func TestANodeAppStartingItselfFromADirectoryWithASpaceStartsThere(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"pnpm-workspace.yaml":      "packages:\n  - \"apps/*\"\n",
		"pnpm-lock.yaml":           "lockfileVersion: '9.0'\n",
		"package.json":             `{"name":"root"}`,
		"apps/my web/package.json": `{"name":"web","main":"my server.js"}`,
	})

	commands, err := located(t, filepath.Join(dir, "apps", "my web"), language.JS).Commands()
	if want := "cd 'apps/my web' && node 'my server.js'"; err != nil || commands.Start != want {
		t.Errorf("Commands() = %+v, %v, want the start %q", commands, err, want)
	}
}

func TestAnAppInNoLanguageJoinsTheJSWorkspaceItSitsIn(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"pnpm-workspace.yaml": "packages:\n  - \"apps/*\"\n",
		"pnpm-lock.yaml":      "lockfileVersion: '9.0'\n",
		"package.json":        `{"name":"root"}`,
		"apps/box/Dockerfile": "FROM scratch\n",
	})

	if got := located(t, filepath.Join(dir, "apps", "box"), ""); got.Root != dir || got.Path != "apps/box" {
		t.Errorf("Locate() = %+v, want the app at apps/box in the workspace at %s: nothing says it is written in a language other than JS", got, dir)
	}
}

func TestAJSAppIgnoresAGoModuleItIsHanded(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"go.mod": "module example.com/shop\n\ngo 1.27.0\n", "web/package.json": `{"name":"web"}`})

	got, err := workspace.Locate(filepath.Join(dir, "web"), workspace.Program{Language: language.JS, GoModule: &gomodule.Module{Dir: dir}, GoPackage: "./web"})
	if err != nil || got.ModuleDir != "" || got.Language != language.JS || got.Root != filepath.Join(dir, "web") {
		t.Errorf("Locate() = %+v, %v, want the JS app at its own directory: a go module is read only for a go app", got, err)
	}
}

func TestAGoModuleReplacingADirectoryOutsideTheRepositoryIsRefusedRatherThanBuiltFromAboveIt(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"elsewhere/sdk/go.mod": "module ocel.dev\n\ngo 1.27.0\n",
		"repo/.git/HEAD":       "ref: refs/heads/main\n",
		"repo/web/go.mod":      "module example.com/web\n\ngo 1.27.0\n\nrequire ocel.dev v0.0.0\n\nreplace ocel.dev => ../../elsewhere/sdk\n",
		"repo/web/main.go":     "package main\n\nfunc main() {}\n",
		"repo/web/lib/doc.go":  "package lib\n",
	})

	_, err := locate(t, filepath.Join(dir, "repo"), filepath.Join(dir, "repo", "web"), language.Go)
	if err == nil {
		t.Fatal("Locate() took a context above the repository the app is checked out in, so the image would be built from whatever else sits beside it")
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, "elsewhere", "sdk")) || !strings.Contains(err.Error(), filepath.Join(dir, "repo")) {
		t.Errorf("Locate() = %v, and the reader is never told which replaced directory lies outside which repository", err)
	}
}

func TestANodeAppInsideAGoModuleIsStillBuiltAsNodeFromItsOwnDirectory(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		".git/HEAD":        "ref: refs/heads/main\n",
		"go.mod":           "module example.com/api\n\ngo 1.27.0\n",
		"web/package.json": `{"name":"web","scripts":{"start":"node server.js"}}`,
		"web/tools.go":     "package tools\n",
	})

	got := located(t, filepath.Join(dir, "web"), language.JS)

	if got.Root != filepath.Join(dir, "web") || got.Language != language.JS {
		t.Errorf("Locate() = %+v, want the node app built from its own directory — the go.mod above it is another app's module", got)
	}
}

func TestAGoAppPointedAtAWiderContextStillBuildsItsPackageWithItsModule(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"repo/.git/HEAD":          "ref: refs/heads/main\n",
		"repo/web/go.mod":         "module example.com/web\n\ngo 1.27.0\n",
		"repo/web/server/main.go": "package main\n\nfunc main() {}\n",
	})

	rebased, err := locatedIn(t, filepath.Join(dir, "repo"), filepath.Join(dir, "repo", "web", "server"), language.Go).Rebase(filepath.Join(dir, "repo"))
	if err != nil {
		t.Fatalf("Rebase() = %v", err)
	}

	if rebased.Root != filepath.Join(dir, "repo") || rebased.Path != "web/server" || rebased.ModuleDir != "web" || rebased.Language != language.Go || rebased.GoVersion != "1.27.0" {
		t.Errorf("Rebase() = %+v, want the app and its module addressed from the context image.context names", rebased)
	}
}
