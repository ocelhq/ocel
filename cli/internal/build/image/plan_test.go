package image_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/shlex"

	"github.com/ocelhq/ocel/cli/internal/build/image"
	"github.com/ocelhq/ocel/cli/internal/gomodule"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/pythonserver"
	"github.com/ocelhq/ocel/cli/internal/workspace"
)

type builtPlan struct {
	Steps []struct {
		Name     string            `json:"name"`
		Assets   map[string]string `json:"assets"`
		Commands []struct {
			Cmd  string `json:"cmd"`
			Src  string `json:"src"`
			Dest string `json:"dest"`
			Path string `json:"path"`
			Name string `json:"name"`
		} `json:"commands"`
		Caches    []string          `json:"caches"`
		Variables map[string]string `json:"variables"`
	} `json:"steps"`
	Deploy struct {
		StartCommand string `json:"startCommand"`
		Inputs       []struct {
			Step    string   `json:"step"`
			Include []string `json:"include"`
			Exclude []string `json:"exclude"`
		} `json:"inputs"`
	} `json:"deploy"`
}

func (p builtPlan) step(t *testing.T, name string) []string {
	t.Helper()
	for _, step := range p.Steps {
		if step.Name != name {
			continue
		}
		var said []string
		for _, command := range step.Commands {
			if command.Cmd != "" {
				said = append(said, command.Cmd)
			}
		}
		return said
	}
	t.Fatalf("the plan has no %q step; it has %d steps", name, len(p.Steps))
	return nil
}

func (p builtPlan) shellScript(t *testing.T, name string) string {
	t.Helper()
	for _, cmd := range p.step(t, name) {
		argv, err := shlex.Split(cmd)
		if err != nil {
			t.Fatalf("split %q as buildkit does: %v", cmd, err)
		}
		if len(argv) > 0 && argv[0] == "sh" {
			if len(argv) != 3 || argv[1] != "-c" {
				t.Fatalf("the %s step runs %q, which buildkit splits into %q rather than one script for sh -c", name, cmd, argv)
			}
			return argv[2]
		}
	}
	t.Fatalf("the %s step runs no shell", name)
	return ""
}

func (p builtPlan) copied(name string) []string {
	var srcs []string
	for _, step := range p.Steps {
		if step.Name != name {
			continue
		}
		for _, command := range step.Commands {
			if command.Cmd == "" && command.Src != "" {
				srcs = append(srcs, command.Src)
			}
		}
	}
	return srcs
}

func located(t *testing.T, dir string, written language.Language) workspace.Location {
	t.Helper()
	return locatedIn(t, dir, dir, written, "")
}

func locatedWith(t *testing.T, dir string, written language.Language, entrypoint string) workspace.Location {
	t.Helper()
	return locatedIn(t, dir, dir, written, entrypoint)
}

func locatedIn(t *testing.T, project, dir string, written language.Language, entrypoint string) workspace.Location {
	t.Helper()
	dir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	program := workspace.Program{Language: written, Entrypoint: entrypoint}
	if found, ok, err := gomodule.FindInProject(dir, project); err == nil && ok && written == language.Go {
		pkg, err := gomodule.PackagePath(found.Dir, dir, entrypoint)
		if err != nil {
			t.Fatalf("PackagePath(%s, %q) = %v", dir, entrypoint, err)
		}
		program.GoModule, program.GoPackage = &found, pkg
	}
	loc, err := workspace.Locate(dir, program)
	if err != nil {
		t.Fatalf("Locate(%s, %s) = %v", project, dir, err)
	}
	return loc
}

func planned(t *testing.T, dir string, written language.Language) builtPlan {
	t.Helper()
	return plannedFrom(t, located(t, dir, written))
}

func plannedFrom(t *testing.T, loc workspace.Location) builtPlan {
	t.Helper()
	raw, err := image.Plan(loc)
	if err != nil {
		t.Fatalf("Plan(%s) = %v", loc.Root, err)
	}
	var plan builtPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatalf("the plan the frontend reads as railpack-plan.json is not JSON: %v", err)
	}
	return plan
}

func assets(t *testing.T, plan builtPlan) string {
	t.Helper()
	var b strings.Builder
	for _, step := range plan.Steps {
		for _, asset := range step.Assets {
			b.WriteString(asset)
		}
	}
	return b.String()
}

func TestAPlainServerIsPlannedWithNothingButItsOwnDirectory(t *testing.T) {
	plan := planned(t, "testdata/plainserver", language.JS)

	if plan.Deploy.StartCommand != "npm run start" {
		t.Errorf("the plan starts the app with %q, want the start script the app already declares", plan.Deploy.StartCommand)
	}
	if !strings.Contains(assets(t, plan), "node = ") {
		t.Errorf("the plan installs no node toolchain, so railpack read a node app as something else:\n%s", assets(t, plan))
	}
}

func TestARailpackFileInTheAppChangesTheBuildOcelNeverReads(t *testing.T) {
	plan := planned(t, "testdata/configured", language.JS)

	if want := "node server.js --from-railpack-json"; plan.Deploy.StartCommand != want {
		t.Errorf("the plan starts the app with %q, want %q from the app's own railpack.json", plan.Deploy.StartCommand, want)
	}
}

func TestThePlanTakesNothingFromTheEnvironmentOcelRunsIn(t *testing.T) {
	t.Setenv("NODE_VERSION", "20")
	t.Setenv("RAILPACK_START_CMD", "node leaked.js")

	plan := planned(t, "testdata/plainserver", language.JS)

	if strings.Contains(assets(t, plan), `node = "20`) {
		t.Errorf("a variable in ocel's own environment pinned the build's node version, so the build is not bare:\n%s", assets(t, plan))
	}
	if plan.Deploy.StartCommand != "npm run start" {
		t.Errorf("the plan starts the app with %q, which came from ocel's environment rather than the app", plan.Deploy.StartCommand)
	}
}

func TestNoVariableOcelRunsUnderAppearsAnywhereInThePlanItHandsTheFrontend(t *testing.T) {
	const leak = "a value the plan must never contain"
	t.Setenv("OCEL_PLAN_LEAK", leak)

	raw, err := image.Plan(located(t, "testdata/plainserver", language.JS))
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}

	for _, secret := range []string{"OCEL_PLAN_LEAK", leak} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the plan the frontend reads contains %q, so ocel's own environment reaches the build through the plan rather than through a build arg:\n%s", secret, raw)
		}
	}
}

func TestADirectoryRailpackCannotReadSaysWhyInsteadOfPlanningNothing(t *testing.T) {
	_, err := image.Plan(located(t, t.TempDir(), language.JS))
	if err == nil {
		t.Fatal("Plan() over an empty directory succeeded, so a build with nothing in it would be attempted")
	}
	if !strings.Contains(err.Error(), "railpack") {
		t.Errorf("Plan() over an empty directory = %v, and the reason never names the builder that refused", err)
	}
}

const workspaceApp = "testdata/pnpmworkspace/apps/web"

func TestAnAppInAWorkspaceInstallsFromTheRootLockfileAndOnlyWhatItReaches(t *testing.T) {
	loc := located(t, workspaceApp, language.JS)
	if loc.Path != "apps/web" || loc.PackageManager != workspace.Pnpm {
		t.Fatalf("Locate(%s) = %+v, want the app located inside the pnpm workspace above it", workspaceApp, loc)
	}
	plan := plannedFrom(t, loc)

	install := strings.Join(plan.step(t, "install"), "\n")
	if want := "pnpm install --frozen-lockfile --filter \"{./apps/web}...\""; !strings.Contains(install, want) {
		t.Errorf("the install step runs:\n%s\nwant %q — the whole monorepo is installed to serve one app otherwise", install, want)
	}
	if strings.Contains(install, "pnpm install --frozen-lockfile --prefer-offline\n") {
		t.Errorf("the install step still runs railpack's unscoped install as well:\n%s", install)
	}

	copied := plan.copied("install")
	for _, want := range []string{"pnpm-lock.yaml", "pnpm-workspace.yaml", "package.json", "apps/web/package.json", "packages/lib/package.json"} {
		if !contains(copied, want) {
			t.Errorf("the install step copies %v, and %q is not among them — pnpm resolves a workspace: range from the root's lockfile and every member's manifest", copied, want)
		}
	}
}

func TestAnAppInAWorkspaceRunsItsOwnScriptsAndNeverTheRoots(t *testing.T) {
	plan := plannedFrom(t, located(t, workspaceApp, language.JS))

	if want := "pnpm --filter \"{./apps/web}\" run start"; plan.Deploy.StartCommand != want {
		t.Errorf("the plan starts the app with %q, want %q", plan.Deploy.StartCommand, want)
	}
	build := strings.Join(plan.step(t, "build"), "\n")
	if strings.Contains(build, "run build") {
		t.Errorf("the build step runs:\n%s\nand the app declares no build script, so that is the workspace root's — which is not this app's build", build)
	}
}

func TestAWorkspaceAppWithABuildScriptBuildsWhatItDependsOnFirst(t *testing.T) {
	loc := located(t, workspaceApp, language.JS)
	loc.Package.Build = true

	plan := plannedFrom(t, loc)

	build := strings.Join(plan.step(t, "build"), "\n")
	if want := "pnpm --filter \"{./apps/web}...\" run build"; !strings.Contains(build, want) {
		t.Errorf("the build step runs:\n%s\nwant %q — a workspace dependency has to be built before the app that imports it", build, want)
	}
}

func pnpmWorkspaceWithAdapter(t *testing.T, libBuild string) string {
	t.Helper()
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		".git/HEAD":                     "ref: refs/heads/main\n",
		"package.json":                  `{"name":"root","private":true,"scripts":{"build":"turbo run build"}}`,
		"pnpm-workspace.yaml":           "packages:\n  - apps/*\n  - packages/*\n",
		"pnpm-lock.yaml":                "lockfileVersion: '9.0'\n\nimporters:\n  .: {}\n  apps/web:\n    devDependencies:\n      '@fixture/adapter':\n        specifier: workspace:^\n        version: link:../../packages/adapter\n  packages/adapter:\n    dependencies:\n      '@fixture/lib':\n        specifier: workspace:^\n        version: link:../lib\n  packages/lib: {}\n",
		"apps/web/package.json":         `{"name":"@fixture/web","scripts":{"build":"vite build","start":"node build"},"devDependencies":{"@fixture/adapter":"workspace:^"}}`,
		"packages/adapter/package.json": `{"name":"@fixture/adapter","scripts":{"build":"tsc"},"dependencies":{"@fixture/lib":"workspace:^"}}`,
		"packages/lib/package.json":     `{"name":"@fixture/lib","scripts":{"build":` + strconv.Quote(libBuild) + `}}`,
	})
	return filepath.Join(root, "apps", "web")
}

func TestAWorkspacePackageTheAppDependsOnThatBuildsWithBunGetsBunInTheImage(t *testing.T) {
	plan := plannedFrom(t, located(t, pnpmWorkspaceWithAdapter(t, "bun build.mjs && tsc"), language.JS))

	if !strings.Contains(assets(t, plan), "bun = ") {
		t.Errorf("the plan installs:\n%s\nand no bun: the build step runs every workspace dependency's build, and one of them runs bun", assets(t, plan))
	}
}

func TestAWorkspaceWhoseBuildsNeverCallBunGetsNoBunInTheImage(t *testing.T) {
	plan := plannedFrom(t, located(t, pnpmWorkspaceWithAdapter(t, "tsc --outDir bundle"), language.JS))

	if strings.Contains(assets(t, plan), "bun = ") {
		t.Errorf("the plan installs:\n%s\nwhich includes bun, and no script the app reaches runs it", assets(t, plan))
	}
}

func TestAnInstallOcelCannotScopeStopsTheBuildRatherThanInstallingTheWholeWorkspace(t *testing.T) {
	loc := located(t, workspaceApp, language.JS)
	loc.PackageManager = workspace.YarnBerry

	_, err := image.Plan(loc)
	if err == nil {
		t.Fatal("Plan() dropped a scoped install it could not place, so the image installs every package in the workspace to serve one app")
	}
	if !strings.Contains(err.Error(), "pnpm install --frozen-lockfile --prefer-offline") {
		t.Errorf("Plan() = %v, and the reader is never told which install command ocel could not replace", err)
	}
}

func TestAConfiguredBuildCommandIsWhatTheImageRuns(t *testing.T) {
	loc := located(t, workspaceApp, language.JS)
	loc.BuildCommand = "turbo run build --filter=@fixture/web"

	plan := plannedFrom(t, loc)

	if build := strings.Join(plan.step(t, "build"), "\n"); !strings.Contains(build, loc.BuildCommand) {
		t.Errorf("the build step runs:\n%s\nwant the image.command the app names", build)
	}
}

func TestScopingAnInstallKeepsTheCachesAndTheSetupRailpackPutAroundIt(t *testing.T) {
	scoped := plannedFrom(t, located(t, workspaceApp, language.JS))
	unscoped := planned(t, "testdata/plainserver", language.JS)

	var install []string
	for _, step := range scoped.Steps {
		if step.Name == "install" {
			install = step.Caches
		}
	}
	if len(install) == 0 {
		t.Error("the scoped install step has no cache, so the package store is re-downloaded on every build")
	}
	if len(scoped.Steps) < len(unscoped.Steps) {
		t.Errorf("the scoped plan has %d steps against %d for an app with no workspace, so scoping dropped a step railpack generated", len(scoped.Steps), len(unscoped.Steps))
	}
}

func TestThePlanNeverCopiesWhatTheContextNoLongerContains(t *testing.T) {
	root := t.TempDir()
	for path, body := range map[string]string{
		"package.json":      `{"name":"solo","scripts":{"start":"node server.js"}}`,
		"package-lock.json": `{"lockfileVersion":3}`,
		"server.js":         "listen()\n",
		"node_modules/better-sqlite3/patches/a.txt": "patch\n",
		"node_modules/better-sqlite3/package.json":  `{"name":"better-sqlite3"}`,
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	plan := planned(t, root, language.JS)

	for _, src := range plan.copied("install") {
		if strings.HasPrefix(filepath.ToSlash(src), "node_modules/") {
			t.Errorf("the install step copies %q, which the build context excludes, so the daemon cannot checksum it and the build fails before it starts", src)
		}
	}
}

func TestANextAppInAWorkspaceIsBuiltAndStartedAsTheAppRatherThanTheRoot(t *testing.T) {
	loc := located(t, "testdata/nextworkspace/apps/web", language.JS)
	if !loc.Member {
		t.Fatalf("Locate() = %+v, want the next app read as a member of the workspace above it", loc)
	}

	plan := plannedFrom(t, loc)

	if want := "pnpm --filter \"{./apps/web}\" run start"; plan.Deploy.StartCommand != want {
		t.Errorf("the plan starts the app with %q, want %q — the root's start script serves nothing", plan.Deploy.StartCommand, want)
	}
	build := strings.Join(plan.step(t, "build"), "\n")
	if want := "pnpm --filter \"{./apps/web}...\" run build"; !strings.Contains(build, want) {
		t.Errorf("the build step runs:\n%s\nwant %q", build, want)
	}
	install := strings.Join(plan.step(t, "install"), "\n")
	if want := "pnpm install --frozen-lockfile --filter \"{./apps/web}...\""; !strings.Contains(install, want) {
		t.Errorf("the install step runs:\n%s\nwant %q", install, want)
	}

	var cached []string
	for _, step := range plan.Steps {
		if step.Name == "build" {
			cached = step.Caches
		}
	}
	if !contains(cached, "next-apps-web") {
		t.Errorf("the build step caches %v, and none of them is the app's own .next: railpack plans the root, and it has to reach into the member to cache what next writes there", cached)
	}

	var included []string
	for _, input := range plan.Deploy.Inputs {
		if input.Step == "build" {
			included = append(included, input.Include...)
		}
	}
	if !contains(included, ".") {
		t.Errorf("the deploy takes %v from the build, and none of it is the directory the app was built in: next writes apps/web/.next, and an image that contains only root-level paths starts without it", included)
	}
}

const polyglotApp = "testdata/polyglotworkspace/apps/web"

func TestANodeAppIsPlannedAsOneEvenWhereAnotherLanguageSitsAtTheRoot(t *testing.T) {
	plan := planned(t, polyglotApp, language.JS)

	install := strings.Join(plan.step(t, "install"), "\n")
	if want := "pnpm install --frozen-lockfile --filter \"{./apps/web}...\""; !strings.Contains(install, want) {
		t.Errorf("the install step runs:\n%s\nwant %q — the root has a go.mod as well, and railpack takes the first language it detects", install, want)
	}
	if want := "pnpm --filter \"{./apps/web}\" run start"; plan.Deploy.StartCommand != want {
		t.Errorf("the plan starts the app with %q, want %q", plan.Deploy.StartCommand, want)
	}
}

const polyglotGoApp = "testdata/polyglotworkspace/apps/api"

func TestAnAppWithAGoModuleIsPlannedAsGoWhateverElseSitsBesideIt(t *testing.T) {
	plan := planned(t, polyglotGoApp, language.Go)

	build := strings.Join(plan.step(t, "build"), "\n")
	if !strings.Contains(build, "go build") {
		t.Errorf("the build step runs:\n%s\nwant a go build — the app has a go.mod, and the package.json and requirements.txt beside it only list what its tooling reads", build)
	}
	if strings.Contains(plan.Deploy.StartCommand, "pnpm") {
		t.Errorf("the plan starts the app with %q, and no package manager starts a compiled binary", plan.Deploy.StartCommand)
	}
}

const polyglotPythonApp = "testdata/polyglotworkspace/apps/svc"

func TestAnAppWithAPythonProjectFileIsPlannedAsPythonWhateverElseSitsBesideIt(t *testing.T) {
	plan := plannedFrom(t, locatedWith(t, polyglotPythonApp, language.Python, "main.py"))

	install := strings.Join(plan.step(t, "install"), "\n")
	if !strings.Contains(install, "pip install") {
		t.Errorf("the install step runs:\n%s\nwant a pip install — the app has a %s, and the package.json beside it only lists the config's own dependencies", install, "requirements.txt")
	}
	if strings.Contains(plan.Deploy.StartCommand, "pnpm") {
		t.Errorf("the plan starts the app with %q, and no package manager starts a python module", plan.Deploy.StartCommand)
	}
	if !strings.Contains(plan.Deploy.StartCommand, "main.py") {
		t.Errorf("the plan starts the app with %q, want the entrypoint the app is served from", plan.Deploy.StartCommand)
	}
}

const polyglotCrateApp = "testdata/polyglotworkspace/apps/crate"

func TestAnAppWithACargoManifestIsPlannedAsRustRatherThanLeftToRailpacksOwnDetection(t *testing.T) {
	plan := planned(t, polyglotCrateApp, language.Rust)

	build := strings.Join(plan.step(t, "build"), "\n")
	if !strings.Contains(build, "cargo build") {
		t.Errorf("the build step runs:\n%s\nwant a cargo build — the app is a crate, and the node workspace it sits inside installs nothing it reads", build)
	}
	if strings.Contains(plan.Deploy.StartCommand, "pnpm") {
		t.Errorf("the plan starts the app with %q, and no package manager starts a compiled binary", plan.Deploy.StartCommand)
	}
	if want := "./bin/crate-app"; plan.Deploy.StartCommand != want {
		t.Errorf("the plan starts the app with %q, want %q — the image execs the one binary the crate builds", plan.Deploy.StartCommand, want)
	}
}

const polyglotStrayScriptApp = "testdata/polyglotworkspace/apps/stray"

func TestANodeAppShippingAScriptBesideItselfIsStillPlannedAsNode(t *testing.T) {
	plan := planned(t, polyglotStrayScriptApp, language.JS)

	install := strings.Join(plan.step(t, "install"), "\n")
	if want := "pnpm install --frozen-lockfile --filter \"{./apps/stray}...\""; !strings.Contains(install, want) {
		t.Errorf("the install step runs:\n%s\nwant %q — the app has a main.py and no python project file, and a script beside a node app is not a python project", install, want)
	}
	if want := "pnpm --filter \"{./apps/stray}\" run start"; plan.Deploy.StartCommand != want {
		t.Errorf("the plan starts the app with %q, want %q", plan.Deploy.StartCommand, want)
	}
}

func TestTheRootsOwnRailpackFileStillShapesAPlanOcelForcesTheProviderOn(t *testing.T) {
	plan := planned(t, polyglotApp, language.JS)

	apt := strings.Join(plan.step(t, "packages:apt:build"), "\n")
	if !strings.Contains(apt, "libvips-dev") {
		t.Errorf("the apt step runs:\n%s\nand the root's %s asks for libvips-dev: naming the provider replaced the file the user wrote rather than adding to it", apt, image.ConfigFileName)
	}
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for path, body := range files {
		named := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(named), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(named, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

const goModuleWithCommands = "module example.com/api\n\ngo 1.25.0\n"

func goAppWithCommands(t *testing.T, extra map[string]string) string {
	t.Helper()
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		".git/HEAD":          "ref: refs/heads/main\n",
		"go.mod":             goModuleWithCommands,
		"lib/lib.go":         "package lib\n",
		"cmd/admin/main.go":  "package main\n\nfunc main() {}\n",
		"cmd/api/main.go":    "package main\n\nfunc main() {}\n",
		"tools/seed/main.go": "package main\n\nfunc main() {}\n",
	})
	writeFiles(t, root, extra)
	return root
}

func TestAGoAppIsBuiltFromThePackageItsEntrypointNamesRatherThanTheCommandRailpackPicks(t *testing.T) {
	for _, entrypoint := range []string{"cmd/api", "tools/seed"} {
		t.Run(entrypoint, func(t *testing.T) {
			plan := plannedFrom(t, locatedWith(t, goAppWithCommands(t, nil), language.Go, entrypoint))

			build := strings.Join(plan.step(t, "build"), "\n")
			if want := "go build -ldflags=\"-w -s\" -o out ./" + entrypoint; !strings.Contains(build, want) {
				t.Errorf("the build step runs:\n%s\nwant %q — railpack alone builds cmd/admin, the first command it finds", build, want)
			}
			if plan.Deploy.StartCommand != "./out" {
				t.Errorf("the plan starts the app with %q, want the binary the build step writes", plan.Deploy.StartCommand)
			}
		})
	}
}

func TestARailpackFileBuildingAGoAppStillChoosesWhatTheImageBuilds(t *testing.T) {
	root := goAppWithCommands(t, map[string]string{
		image.ConfigFileName: `{"steps":{"build":{"commands":["go build -o out ./cmd/admin"]}},"deploy":{"startCommand":"./out --admin"}}`,
	})

	plan := plannedFrom(t, locatedWith(t, root, language.Go, "cmd/api"))

	build := strings.Join(plan.step(t, "build"), "\n")
	if !strings.Contains(build, "go build -o out ./cmd/admin") || strings.Contains(build, "./cmd/api") {
		t.Errorf("the build step runs:\n%s\nwant only the build the app's %s names", build, image.ConfigFileName)
	}
	if plan.Deploy.StartCommand != "./out --admin" {
		t.Errorf("the plan starts the app with %q, want the start command the app's %s names", plan.Deploy.StartCommand, image.ConfigFileName)
	}
}

func TestARustAppIsBuiltAndStartedFromTheBinaryItsEntrypointNamesWithoutCargoOnTheHost(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"Cargo.toml":  "[package]\nname = \"server\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[[bin]]\nname = \"server\"\npath = \"src/main.rs\"\n\n[[bin]]\nname = \"api\"\npath = \"src/api.rs\"\n",
		"src/main.rs": "fn main() {}\n",
		"src/api.rs":  "fn main() {}\n",
	})

	plan := plannedFrom(t, locatedWith(t, root, language.Rust, "api"))

	build := strings.Join(plan.step(t, "build"), "\n")
	if !strings.Contains(build, "cargo build --release --bin api") {
		t.Errorf("the build step runs:\n%s\nwant only the api binary built, as the function build does", build)
	}
	if plan.Deploy.StartCommand != "./bin/api" {
		t.Errorf("the plan starts the app with %q, want ./bin/api — the binary the entrypoint names", plan.Deploy.StartCommand)
	}
}

const twoBinCrateManifest = "[package]\nname = \"server\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[workspace]\n"

func TestARustAppNamingNoEntrypointIsLeftToRailpackWhichStartsTheBinaryTheCrateRunsByDefault(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"Cargo.toml":     strings.Replace(twoBinCrateManifest, "[package]\n", "[package]\ndefault-run = \"api\"\n", 1),
		"src/main.rs":    "fn main() {}\n",
		"src/bin/api.rs": "fn main() {}\n",
	})

	plan := planned(t, root, language.Rust)

	if build := strings.Join(plan.step(t, "build"), "\n"); strings.Contains(build, "--bin") {
		t.Errorf("the build step runs:\n%s\nwant railpack's own build of every binary: the app names none", build)
	}
	if plan.Deploy.StartCommand != "./bin/api" {
		t.Errorf("the plan starts the app with %q, want ./bin/api — the binary default-run names", plan.Deploy.StartCommand)
	}
}

func TestARustAppBuildingSeveralBinariesAndNamingNoneIsRefusedBeforeItsImageIsBuilt(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"Cargo.toml":     twoBinCrateManifest,
		"src/main.rs":    "fn main() {}\n",
		"src/bin/api.rs": "fn main() {}\n",
	})

	if _, err := image.Plan(located(t, root, language.Rust)); err == nil || !strings.Contains(err.Error(), "start command") {
		t.Fatalf("Plan() = %v, want railpack's refusal to plan an image it has no command to start", err)
	}
}

func TestAGoAppBelowItsModuleIsBuiltFromThePackageWithTheModulesItReplacesAndNothingTheGoWorkUses(t *testing.T) {
	root := t.TempDir()
	for path, body := range map[string]string{
		".git/HEAD":               "ref: refs/heads/main\n",
		"go.work":                 "go 1.25.0\n\nuse (\n\t./sdk\n\t./tools\n)\n",
		"sdk/go.mod":              "module ocel.dev\n\ngo 1.25.0\n",
		"sdk/sdk.go":              "package ocel\n",
		"tools/go.mod":            "module example.com/tools\n\ngo 1.25.0\n",
		"apps/web/go.mod":         "module example.com/web\n\ngo 1.27.0\n\nrequire ocel.dev v0.0.0\n\nreplace ocel.dev => ../../sdk\n",
		"apps/web/server/main.go": "package main\n\nfunc main() {}\n",
	} {
		named := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(named), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(named, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	plan := plannedFrom(t, locatedIn(t, root, filepath.Join(root, "apps", "web", "server"), language.Go, ""))

	build := strings.Join(plan.step(t, "build"), "\n")
	if !strings.Contains(build, "cd apps/web && GOWORK=off CGO_ENABLED=0 go build -ldflags=\"-w -s\" -o ../../out ./server") {
		t.Errorf("the build step runs:\n%s\nwant go build run in the app's module, of the app's package", build)
	}
	install := plan.step(t, "install")
	if !contains(install, "sh -c 'cd apps/web && GOWORK=off go mod download'") || contains(install, "go mod download") {
		t.Errorf("the install step runs %q, want only the app's module downloaded — the go.work at the context root uses modules the app never imports", install)
	}
	for _, want := range []string{"apps/web/go.mod", "sdk/go.mod"} {
		if !contains(plan.copied("install"), want) {
			t.Errorf("the install step copies %v, and %q is not among them — the module and every module it replaces are read by go mod download", plan.copied("install"), want)
		}
	}
	if !strings.Contains(assets(t, plan), `go = "1.27.0"`) {
		t.Errorf("the plan installs go as:\n%s\nwant the 1.27.0 the app's own module asks for, not the go.work's", assets(t, plan))
	}
	if plan.Deploy.StartCommand != "./out" {
		t.Errorf("the plan starts the app with %q, want the binary the build step writes at the context root", plan.Deploy.StartCommand)
	}
}

func TestARustAppReachingACrateByPathIsCompiledInTheBuildStepFromTheDirectoryHoldingBoth(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		".git/HEAD":                      "ref: refs/heads/main\n",
		"crates/Cargo.toml":              "[workspace]\nmembers = [\"*\"]\n\n[workspace.package]\nversion = \"0.1.0\"\n",
		"crates/ocel-sdk/Cargo.toml":     "[package]\nname = \"ocel-sdk\"\nversion.workspace = true\n",
		"crates/ocel-sdk/src/lib.rs":     "\n",
		"tests/fixtures/web/Cargo.toml":  "[package]\nname = \"web\"\nversion = \"0.1.0\"\nedition = \"2021\"\nrust-version = \"1.92\"\n\n[workspace]\n\n[dependencies]\nocel-sdk = { path = \"../../../crates/ocel-sdk\" }\n",
		"tests/fixtures/web/src/main.rs": "fn main() {}\n",
	})

	plan := plannedFrom(t, locatedIn(t, root, filepath.Join(root, "tests", "fixtures", "web"), language.Rust, ""))

	if install := plan.step(t, "install"); len(install) != 0 {
		t.Errorf("the install step runs %q, want nothing — the context root holds no Cargo.toml for it to compile dependencies from", install)
	}
	build := strings.Join(plan.step(t, "build"), "\n")
	if want := "cd tests/fixtures/web && mkdir -p bin && cargo build --release --bin web --target-dir ../../../target"; !strings.Contains(build, want) {
		t.Errorf("the build step runs:\n%s\nwant %q — the crate is compiled from its own directory, where cargo finds the sdk by its path", build, want)
	}
	if plan.Deploy.StartCommand != "./tests/fixtures/web/bin/web" {
		t.Errorf("the plan starts the app with %q, want the crate's one binary", plan.Deploy.StartCommand)
	}
	if !strings.Contains(assets(t, plan), `rust = "1.92`) {
		t.Errorf("the plan installs:\n%s\nwant the rust 1.92 the crate requires — no Cargo.toml at the context root tells railpack", assets(t, plan))
	}
}

func TestAPythonAppWithAUvPathSourceIsSyncedInTheBuildStepFromTheDirectoryHoldingBoth(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		".git/HEAD":                  "ref: refs/heads/main\n",
		"python/ocel/pyproject.toml": "[project]\nname = \"ocel\"\nversion = \"0.0.0\"\n",
		"apps/web/pyproject.toml":    "[project]\nname = \"web\"\nversion = \"0.0.0\"\ndependencies = [\"ocel\"]\n\n[tool.uv.sources]\nocel = { path = \"../../python/ocel\", editable = true }\n",
		"apps/web/uv.lock":           "version = 1\n",
		"apps/web/.python-version":   "3.12\n",
		"apps/web/main.py":           "print('hi')\n",
	})

	plan := plannedFrom(t, locatedIn(t, root, filepath.Join(root, "apps", "web"), language.Python, "main.py"))

	if install := plan.step(t, "install"); len(install) != 0 {
		t.Errorf("the install step runs %q, want nothing — the context root holds no pyproject.toml for it to sync", install)
	}
	if want := "cd apps/web && UV_PYTHON_DOWNLOADS=never uv sync --locked --no-dev --no-editable"; plan.shellScript(t, "build") != want {
		t.Errorf("the build step's shell runs %q, want %q — the app is synced from its own directory, where uv finds the sdk by its path", plan.shellScript(t, "build"), want)
	}
	if !strings.Contains(assets(t, plan), "uv = ") {
		t.Errorf("the plan installs:\n%s\nwant uv among them — nothing at the context root tells railpack the build runs it", assets(t, plan))
	}
	if !strings.Contains(assets(t, plan), `python = "3.12`) {
		t.Errorf("the plan installs:\n%s\nwant the python 3.12 the app's .python-version names — uv sync downloads no other", assets(t, plan))
	}
	if want := "cd apps/web && .venv/bin/python3 ocel-server.py file main.py"; plan.Deploy.StartCommand != want {
		t.Errorf("the plan starts the app with %q, want %q — the python the sync installed the app's dependencies for", plan.Deploy.StartCommand, want)
	}
}

func TestAPythonAppWithAUvPathSourceRunsTheBuildCommandItNamesAfterTheSync(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		".git/HEAD":                  "ref: refs/heads/main\n",
		"python/ocel/pyproject.toml": "[project]\nname = \"ocel\"\nversion = \"0.0.0\"\n",
		"apps/web/pyproject.toml":    "[project]\nname = \"web\"\nversion = \"0.0.0\"\n\n[tool.uv.sources]\nocel = { path = \"../../python/ocel\" }\n",
		"apps/web/uv.lock":           "version = 1\n",
		"apps/web/main.py":           "print('hi')\n",
	})
	loc := locatedIn(t, root, filepath.Join(root, "apps", "web"), language.Python, "main.py")
	loc.BuildCommand = "python -c 1"

	plan := plannedFrom(t, loc)

	if want := "cd apps/web && UV_PYTHON_DOWNLOADS=never uv sync --locked --no-dev --no-editable && python -c 1"; plan.shellScript(t, "build") != want {
		t.Errorf("the build step's shell runs %q, want %q — the image starts the python the sync writes, so the build command runs after it rather than in its place", plan.shellScript(t, "build"), want)
	}
}

func TestAnAppInADirectoryItsBuildQuotesIsBuiltThereByTheShellTheImageRuns(t *testing.T) {
	for _, tt := range []struct {
		name    string
		files   map[string]string
		app     string
		written language.Language
		entry   string
		want    string
	}{
		{
			name: "a rust workspace member",
			files: map[string]string{
				"Cargo.toml":            "[workspace]\nmembers = [\"my api\"]\n",
				"my api/Cargo.toml":     "[package]\nname = \"api\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
				"my api/src/main.rs":    "fn main() {}\n",
				"my api/src/bin/api.rs": "fn main() {}\n",
			},
			app:     "my api",
			written: language.Rust,
			entry:   "api",
			want:    "cd 'my api' && mkdir -p bin && cargo build --release --bin api --target-dir ../target && cp ../target/release/api bin/",
		},
		{
			name: "a go package below its module",
			files: map[string]string{
				"go.mod":         "module example.com/web\n\ngo 1.25.0\n",
				"my web/main.go": "package main\n\nfunc main() {}\n",
			},
			app:     "my web",
			written: language.Go,
			entry:   ".",
			want:    `GOWORK=off CGO_ENABLED=0 go build -ldflags="-w -s" -o out './my web'`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.files[".git/HEAD"] = "ref: refs/heads/main\n"
			writeFiles(t, root, tt.files)

			plan := plannedFrom(t, locatedIn(t, root, filepath.Join(root, tt.app), tt.written, tt.entry))

			if got := plan.shellScript(t, "build"); got != tt.want {
				t.Errorf("the build step's shell runs %q, want %q — railpack wraps the build command in single quotes, so a quote inside it must reach sh intact", got, tt.want)
			}
		})
	}
}

func contains(haystack []string, needle string) bool {
	for _, straw := range haystack {
		if straw == needle {
			return true
		}
	}
	return false
}

func pythonProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func describedPython(t *testing.T, app string, files map[string]string) image.App {
	t.Helper()
	files[project.DefaultFileName] = `{"slug":"shop","apps":[` + app + `]}`
	cfg, err := project.Load(context.Background(), pythonProject(t, files), "")
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	described, err := image.Describe(cfg, cfg.Apps[0])
	if err != nil {
		t.Fatalf("Describe() = %v", err)
	}
	return described
}

func plannedPython(t *testing.T, app string, files map[string]string) builtPlan {
	t.Helper()
	described := describedPython(t, app, files)
	return plannedFrom(t, described.Workspace)
}

func (p builtPlan) writes(step, path string) (string, bool) {
	for _, planned := range p.Steps {
		if planned.Name != step {
			continue
		}
		for _, command := range planned.Commands {
			if command.Name != "" && command.Path == path {
				asset, ok := planned.Assets[command.Name]
				return asset, ok
			}
		}
	}
	return "", false
}

func TestAPythonImageStartsTheDetectedEntrypointThroughOcelsServerProgram(t *testing.T) {
	plan := plannedPython(t, `{"name":"api","path":".","compute":"container"}`, map[string]string{
		"requirements.txt": "flask==3.1.1\n",
		"src/app.py":       "app = None\n",
		"src/main.py":      "print('hi')\n",
	})

	if want := "python3 ocel-server.py file src/app.py"; plan.Deploy.StartCommand != want {
		t.Errorf("the plan starts the app with %q, want %q — an image serves the entrypoint the project detected, as the function does", plan.Deploy.StartCommand, want)
	}
	written, ok := plan.writes("build", "/app/"+pythonserver.File)
	if !ok || written != pythonserver.Source {
		t.Errorf("the build step writes no %s holding the server program beside the app, and the start command runs it", pythonserver.File)
	}
}

func TestAPythonImageServesTheEntrypointTheAppNames(t *testing.T) {
	plan := plannedPython(t, `{"name":"api","path":".","entrypoint":"shop.api:app","compute":"container"}`, map[string]string{
		"requirements.txt": "flask==3.1.1\n",
		"main.py":          "print('hi')\n",
	})

	if want := "python3 ocel-server.py module shop.api:app"; plan.Deploy.StartCommand != want {
		t.Errorf("the plan starts the app with %q, want %q", plan.Deploy.StartCommand, want)
	}
}

func TestAPythonImageBuiltFromAContextAboveTheAppStartsInTheAppsDirectory(t *testing.T) {
	plan := plannedPython(t, `{"name":"api","path":"server dir","compute":{"container":{"image":{"context":"."}}}}`, map[string]string{
		"server dir/main.py":          "print('hi')\n",
		"server dir/requirements.txt": "flask==3.1.1\n",
	})

	if want := "cd 'server dir' && python3 ocel-server.py file main.py"; plan.Deploy.StartCommand != want {
		t.Errorf("the plan starts the app with %q, want %q", plan.Deploy.StartCommand, want)
	}
	if _, ok := plan.writes("build", "/app/server dir/"+pythonserver.File); !ok {
		t.Errorf("the build step writes no server program in the app's own directory under the context")
	}
}

func TestAPythonAppsOwnRailpackStartCommandIsWhatItsImageRuns(t *testing.T) {
	plan := plannedPython(t, `{"name":"api","path":".","compute":"container"}`, map[string]string{
		"requirements.txt": "gunicorn==23.0.0\n",
		"main.py":          "app = None\n",
		"railpack.json":    `{"deploy": {"startCommand": "gunicorn main:app"}}`,
	})

	if plan.Deploy.StartCommand != "gunicorn main:app" {
		t.Errorf("the plan starts the app with %q, want the start command its railpack.json names", plan.Deploy.StartCommand)
	}
}

func TestTheImageOfAPythonAppWithNoEntrypointIsLeftToRailpack(t *testing.T) {
	plan := plannedPython(t, `{"name":"site","path":".","compute":"container"}`, map[string]string{
		"requirements.txt":   "django==5.2\ngunicorn==23.0.0\n",
		"manage.py":          "\n",
		"mysite/settings.py": "WSGI_APPLICATION = 'mysite.wsgi.application'\n",
		"mysite/wsgi.py":     "application = None\n",
	})

	if want := "mysite.wsgi:application"; !strings.Contains(plan.Deploy.StartCommand, want) || strings.Contains(plan.Deploy.StartCommand, pythonserver.File) {
		t.Errorf("the plan starts the app with %q, want railpack's own start of the Django app (%s): no entrypoint was resolved for ocel's server program to serve", plan.Deploy.StartCommand, want)
	}
	if _, ok := plan.writes("build", "/app/"+pythonserver.File); ok {
		t.Errorf("the build step writes %s, which nothing starts", pythonserver.File)
	}
}
