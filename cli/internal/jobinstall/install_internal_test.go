package jobinstall

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/workspace"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const (
	withDependencies = `{"dependencies":{"ocel":"^0.0.4"}}`
	infra            = discovery.DefaultRootDirName
)

func TestAJSProjectMissingItsDependenciesInstallsThemFrozenAsOcelWould(t *testing.T) {
	for _, tt := range []struct {
		name    string
		files   map[string]string
		project string
		dir     string
		want    []string
	}{
		{"npm", map[string]string{"package.json": withDependencies, "package-lock.json": "{}"}, "", "", []string{"npm", "ci"}},
		{"npm's shrinkwrap", map[string]string{"package.json": withDependencies, "npm-shrinkwrap.json": "{}"}, "", "", []string{"npm", "ci"}},
		{"pnpm", map[string]string{"package.json": withDependencies, "pnpm-lock.yaml": ""}, "", "", []string{"pnpm", "install", "--frozen-lockfile"}},
		{"bun", map[string]string{"package.json": withDependencies, "bun.lock": ""}, "", "", []string{"bun", "install", "--frozen-lockfile"}},
		{"bun's binary lockfile", map[string]string{"package.json": withDependencies, "bun.lockb": ""}, "", "", []string{"bun", "install", "--frozen-lockfile"}},
		{"a v1 yarn.lock with no packageManager", map[string]string{"package.json": withDependencies, "yarn.lock": "# yarn lockfile v1\n"}, "", "", []string{"corepack", "yarn@1.22.22", "install", "--frozen-lockfile"}},
		{"yarn classic declared", map[string]string{"package.json": `{"packageManager":"yarn@1.22.19","devDependencies":{"ocel":"*"}}`, "yarn.lock": ""}, "", "", []string{"corepack", "yarn@1.22.22", "install", "--frozen-lockfile"}},
		{"yarn berry declared", map[string]string{"package.json": `{"packageManager":"yarn@4.5.0","dependencies":{"ocel":"*"}}`, "yarn.lock": ""}, "", "", []string{"yarn", "install", "--immutable"}},
		{"yarn berry by its .yarnrc.yml", map[string]string{"package.json": withDependencies, "yarn.lock": "", ".yarnrc.yml": "nodeLinker: pnp\n"}, "", "", []string{"yarn", "install", "--immutable"}},
		{"yarn berry by its lockfile's header", map[string]string{"package.json": withDependencies, "yarn.lock": "__metadata:\n  version: 8\n"}, "", "", []string{"yarn", "install", "--immutable"}},
		{"the packageManager declared over the lockfiles' order", map[string]string{"package.json": `{"packageManager":"npm@10.9.0","dependencies":{"ocel":"*"}}`, "pnpm-lock.yaml": "", "package-lock.json": "{}"}, "", "", []string{"npm", "ci"}},
		{"the lockfiles' order with none declared", map[string]string{"package.json": withDependencies, "pnpm-lock.yaml": "", "package-lock.json": "{}"}, "", "", []string{"pnpm", "install", "--frozen-lockfile"}},
		{"a lockfile alone over the packageManager declared", map[string]string{"package.json": `{"packageManager":"pnpm@9.0.0","dependencies":{"ocel":"*"}}`, "package-lock.json": "{}"}, "", "", []string{"npm", "ci"}},
		{"a member of a workspace, at its root", map[string]string{"package.json": `{"workspaces":["apps/*"]}`, "pnpm-lock.yaml": "", "apps/web/package.json": withDependencies}, "apps/web", "", []string{"pnpm", "install", "--frozen-lockfile"}},
		{"a member of a pnpm workspace, at its root", map[string]string{"package.json": "{}", "pnpm-workspace.yaml": "packages:\n  - 'apps/**'\n", "pnpm-lock.yaml": "", "apps/site/web/package.json": withDependencies}, "apps/site/web", "", []string{"pnpm", "install", "--frozen-lockfile"}},
		{"a member whose root alone declares dependencies", map[string]string{"package.json": `{"workspaces":{"packages":["apps/*"]},"devDependencies":{"ocel":"*"}}`, "package-lock.json": "{}", "apps/web/package.json": "{}"}, "apps/web", "", []string{"npm", "ci"}},
		{"a package the workspace excludes, in its own directory", map[string]string{"package.json": `{"workspaces":["apps/*","!apps/legacy"]}`, "pnpm-lock.yaml": "", "apps/legacy/package.json": withDependencies, "apps/legacy/package-lock.json": "{}"}, "apps/legacy", "apps/legacy", []string{"npm", "ci"}},
		{"a project below its package", map[string]string{"package.json": withDependencies, "bun.lock": "", infra + "/index.ts": ""}, infra, "", []string{"bun", "install", "--frozen-lockfile"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source := t.TempDir()
			for name, content := range tt.files {
				write(t, filepath.Join(source, name), content)
			}
			missing, ok, err := findInstall(source, filepath.Join(source, tt.project))
			if err != nil || !ok {
				t.Fatalf("findInstall = %v, %v; want %q", ok, err, tt.want)
			}
			if dir := filepath.Join(source, tt.dir); missing.Dir != dir || !slices.Equal(missing.Args, tt.want) {
				t.Errorf("findInstall = %q in %s, want %q in %s", missing.Args, missing.Dir, tt.want, dir)
			}
		})
	}
}

func TestTheJobImageHoldsTheYarnClassicAJobRuns(t *testing.T) {
	dockerfile, err := os.ReadFile(filepath.Join("..", "..", "jobimage", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	classic := frozenInstalls[workspace.YarnClassic]
	if classic[0] != "corepack" || !strings.Contains(string(dockerfile), "corepack prepare "+classic[1]+" ") {
		t.Fatalf("yarn classic installs with %q, which the job image does not prepare", classic)
	}
}

func TestAProjectWithNothingToInstallOrItsDependenciesInstalledInstallsNothing(t *testing.T) {
	for _, tt := range []struct {
		name    string
		files   map[string]string
		project string
	}{
		{"no package.json", map[string]string{"go.mod": "module example.com/app\n"}, ""},
		{"no dependencies", map[string]string{"package.json": `{"name":"web"}`, "package-lock.json": "{}"}, ""},
		{"node_modules in the package", map[string]string{"package.json": withDependencies, "package-lock.json": "{}", "node_modules/.package-lock.json": "{}"}, ""},
		{"node_modules above the package", map[string]string{"package.json": `{"workspaces":["apps/*"]}`, "package-lock.json": "{}", "node_modules/.package-lock.json": "{}", "apps/web/package.json": withDependencies}, "apps/web"},
		{"yarn's .pnp.cjs", map[string]string{"package.json": withDependencies, "yarn.lock": "__metadata:\n", ".pnp.cjs": ""}, ""},
		{"yarn's .pnp.js", map[string]string{"package.json": withDependencies, "yarn.lock": "__metadata:\n", ".pnp.js": ""}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source := t.TempDir()
			for name, content := range tt.files {
				write(t, filepath.Join(source, name), content)
			}
			if missing, ok, err := findInstall(source, filepath.Join(source, tt.project)); ok || err != nil {
				t.Errorf("findInstall = %q, %v, %v; want nothing to install", missing.Args, ok, err)
			}
		})
	}
}

func TestDependenciesWithNoLockfileAreRefusedWithWhereToCommitOne(t *testing.T) {
	for _, tt := range []struct {
		name    string
		files   map[string]string
		project string
		where   string
	}{
		{"a package", map[string]string{"package.json": withDependencies}, "", "the project's root"},
		{"a workspace member", map[string]string{"package.json": `{"workspaces":["apps/*"]}`, "apps/web/package.json": withDependencies}, "apps/web", "the project's root"},
		{"a package outside the workspace above it", map[string]string{"package.json": `{"workspaces":["apps/*"]}`, "package-lock.json": "{}", "tools/web/package.json": withDependencies}, "tools/web", "tools/web"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source := t.TempDir()
			for name, content := range tt.files {
				write(t, filepath.Join(source, name), content)
			}
			_, ok, err := findInstall(source, filepath.Join(source, tt.project))
			if ok || err == nil || !strings.Contains(err.Error(), "install them in "+tt.where) || !strings.Contains(err.Error(), "commit the lockfile") {
				t.Fatalf("findInstall = %v, %v; want it refused, naming %s", ok, err, tt.where)
			}
		})
	}
}

func TestNothingAboveTheSourceCounts(t *testing.T) {
	outside := t.TempDir()
	write(t, filepath.Join(outside, "package.json"), `{"workspaces":["source"]}`)
	write(t, filepath.Join(outside, "package-lock.json"), "{}")
	source := filepath.Join(outside, "source")
	write(t, filepath.Join(source, "package.json"), withDependencies)
	if _, ok, err := findInstall(source, source); ok || err == nil {
		t.Fatalf("findInstall = %v, %v; want the lockfile above the source never used", ok, err)
	}

	write(t, filepath.Join(source, "package-lock.json"), "{}")
	write(t, filepath.Join(outside, "node_modules", ".package-lock.json"), "{}")
	if missing, ok, err := findInstall(source, source); !ok || err != nil || missing.Dir != source {
		t.Fatalf("findInstall = %+v, %v, %v; want an install in the source, whatever is installed above it", missing, ok, err)
	}
}

func TestTheInstallsAreThoseOfTheProjectsJSDiscoveryRoots(t *testing.T) {
	root := map[string]string{"package.json": withDependencies}
	locked := map[string]string{"package.json": withDependencies, "package-lock.json": "{}"}
	for _, tt := range []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"Go infra beside a package with no lockfile", with(root, infra+"/main.go", "package infra\n"), nil},
		{"Python infra beside a package with no lockfile", with(root, infra+"/resources.py", ""), nil},
		{"a lockfile and no infra", locked, nil},
		{"a lockfile and Go infra", with(locked, infra+"/main.go", "package infra\n"), nil},
		{"JS infra whose tests alone are Go", with(locked, infra+"/index.ts", "", infra+"/infra_test.go", ""), []string{"."}},
		{"JS infra with a package of its own", with(root, infra+"/index.ts", "", infra+"/package.json", withDependencies, infra+"/pnpm-lock.yaml", ""), []string{infra}},
		{"JS infra of the root's package", with(locked, infra+"/index.ts", ""), []string{"."}},
		{"discovery paths the config names", with(locked, "ocel.json", `{"slug":"web","discovery":{"paths":["declare","services/*/`+infra+`"]}}`,
			"declare/index.ts", "", "services/api/"+infra+"/index.ts", "", "services/api/"+infra+"/package.json", withDependencies, "services/api/"+infra+"/bun.lock", "",
			"services/web/"+infra+"/main.go", "package infra\n", infra+"/index.ts", ""), []string{".", "services/api/" + infra}},
		{"discovery paths of a YAML config", with(locked, "ocel.yaml", "slug: web\ndiscovery:\n  paths: [declare]\n", "declare/index.ts", ""), []string{"."}},
		{"no discovery paths", with(locked, "ocel.json", `{"slug":"web","discovery":{"paths":[]}}`, infra+"/index.ts", ""), nil},
		{"a TypeScript config, which ocel evaluates in the project", with(locked, "ocel.config.ts", "export default {}\n"), []string{"."}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source := t.TempDir()
			for name, content := range tt.files {
				write(t, filepath.Join(source, name), content)
			}
			missing, err := Find(t.Context(), source, source)
			if err != nil {
				t.Fatalf("Find = %v; want %q", err, tt.want)
			}
			var dirs []string
			for _, install := range missing {
				rel, _ := filepath.Rel(source, install.Dir)
				dirs = append(dirs, filepath.ToSlash(rel))
			}
			if !slices.Equal(dirs, tt.want) {
				t.Errorf("Find installs in %q, want %q", dirs, tt.want)
			}
		})
	}
}

func with(files map[string]string, more ...string) map[string]string {
	all := maps.Clone(files)
	for i := 0; i+1 < len(more); i += 2 {
		all[more[i]] = more[i+1]
	}
	return all
}
