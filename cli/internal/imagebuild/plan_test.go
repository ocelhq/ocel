package imagebuild_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/imagebuild"
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
		} `json:"commands"`
		Caches []string `json:"caches"`
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

func located(t *testing.T, dir string) workspace.Location {
	t.Helper()
	loc, err := workspace.Locate(dir)
	if err != nil {
		t.Fatalf("Locate(%s) = %v", dir, err)
	}
	return loc
}

func planned(t *testing.T, dir string) builtPlan {
	t.Helper()
	return plannedFrom(t, located(t, dir))
}

func plannedFrom(t *testing.T, loc workspace.Location) builtPlan {
	t.Helper()
	raw, err := imagebuild.Plan(loc)
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
	plan := planned(t, "testdata/plainserver")

	if plan.Deploy.StartCommand != "npm run start" {
		t.Errorf("the plan starts the app with %q, want the start script the app already declares", plan.Deploy.StartCommand)
	}
	if !strings.Contains(assets(t, plan), "node = ") {
		t.Errorf("the plan installs no node toolchain, so railpack read a node app as something else:\n%s", assets(t, plan))
	}
}

func TestARailpackFileInTheAppChangesTheBuildOcelNeverReads(t *testing.T) {
	plan := planned(t, "testdata/configured")

	if want := "node server.js --from-railpack-json"; plan.Deploy.StartCommand != want {
		t.Errorf("the plan starts the app with %q, want %q from the app's own railpack.json", plan.Deploy.StartCommand, want)
	}
}

func TestThePlanCarriesNothingFromTheEnvironmentOcelRunsIn(t *testing.T) {
	t.Setenv("NODE_VERSION", "20")
	t.Setenv("RAILPACK_START_CMD", "node leaked.js")

	plan := planned(t, "testdata/plainserver")

	if strings.Contains(assets(t, plan), `node = "20`) {
		t.Errorf("a variable in ocel's own environment pinned the build's node version, so the build is not bare:\n%s", assets(t, plan))
	}
	if plan.Deploy.StartCommand != "npm run start" {
		t.Errorf("the plan starts the app with %q, which came from ocel's environment rather than the app", plan.Deploy.StartCommand)
	}
}

func TestNoVariableOcelRunsUnderAppearsAnywhereInThePlanItHandsTheFrontend(t *testing.T) {
	const leak = "a value the plan must never carry"
	t.Setenv("OCEL_PLAN_LEAK", leak)

	raw, err := imagebuild.Plan(located(t, "testdata/plainserver"))
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}

	for _, secret := range []string{"OCEL_PLAN_LEAK", leak} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the plan the frontend reads carries %q, so ocel's own environment reaches the build through the plan rather than through a build arg:\n%s", secret, raw)
		}
	}
}

func TestADirectoryRailpackCannotReadSaysWhyInsteadOfPlanningNothing(t *testing.T) {
	_, err := imagebuild.Plan(located(t, t.TempDir()))
	if err == nil {
		t.Fatal("Plan() over an empty directory succeeded, so a build with nothing in it would be attempted")
	}
	if !strings.Contains(err.Error(), "railpack") {
		t.Errorf("Plan() over an empty directory = %v, and the reason never names the builder that refused", err)
	}
}

const workspaceApp = "testdata/pnpmworkspace/apps/web"

func TestAnAppInAWorkspaceInstallsFromTheRootLockfileAndOnlyWhatItReaches(t *testing.T) {
	loc := located(t, workspaceApp)
	if loc.Path != "apps/web" || loc.Manager != workspace.Pnpm {
		t.Fatalf("Locate(%s) = %+v, want the app located inside the pnpm workspace above it", workspaceApp, loc)
	}
	plan := plannedFrom(t, loc)

	install := strings.Join(plan.step(t, "install"), "\n")
	if want := "pnpm install --frozen-lockfile --filter ./apps/web..."; !strings.Contains(install, want) {
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
	plan := plannedFrom(t, located(t, workspaceApp))

	if want := "pnpm --filter ./apps/web run start"; plan.Deploy.StartCommand != want {
		t.Errorf("the plan starts the app with %q, want %q", plan.Deploy.StartCommand, want)
	}
	build := strings.Join(plan.step(t, "build"), "\n")
	if strings.Contains(build, "run build") {
		t.Errorf("the build step runs:\n%s\nand the app declares no build script, so that is the workspace root's — which is not this app's build", build)
	}
}

func TestAWorkspaceAppWithABuildScriptBuildsWhatItDependsOnFirst(t *testing.T) {
	loc := located(t, workspaceApp)
	loc.App.Build = true

	plan := plannedFrom(t, loc)

	build := strings.Join(plan.step(t, "build"), "\n")
	if want := "pnpm --filter ./apps/web... run build"; !strings.Contains(build, want) {
		t.Errorf("the build step runs:\n%s\nwant %q — a workspace dependency has to be built before the app that imports it", build, want)
	}
}

func TestAnInstallOcelCannotScopeStopsTheBuildRatherThanInstallingTheWholeWorkspace(t *testing.T) {
	loc := located(t, workspaceApp)
	loc.Manager = workspace.YarnBerry

	_, err := imagebuild.Plan(loc)
	if err == nil {
		t.Fatal("Plan() dropped a scoped install it could not place, so the image installs every package in the workspace to serve one app")
	}
	if !strings.Contains(err.Error(), "pnpm install --frozen-lockfile --prefer-offline") {
		t.Errorf("Plan() = %v, and the reader is never told which install command ocel could not replace", err)
	}
}

func TestAConfiguredBuildCommandIsWhatTheImageRuns(t *testing.T) {
	loc := located(t, workspaceApp)
	loc.BuildCommand = "turbo run build --filter=@fixture/web"

	plan := plannedFrom(t, loc)

	if build := strings.Join(plan.step(t, "build"), "\n"); !strings.Contains(build, loc.BuildCommand) {
		t.Errorf("the build step runs:\n%s\nwant the build.command the app names", build)
	}
}

func TestScopingAnInstallKeepsTheCachesAndTheSetupRailpackPutAroundIt(t *testing.T) {
	scoped := plannedFrom(t, located(t, workspaceApp))
	unscoped := planned(t, "testdata/plainserver")

	var install []string
	for _, step := range scoped.Steps {
		if step.Name == "install" {
			install = step.Caches
		}
	}
	if len(install) == 0 {
		t.Error("the scoped install step carries no cache, so the package store is re-downloaded on every build")
	}
	if len(scoped.Steps) < len(unscoped.Steps) {
		t.Errorf("the scoped plan has %d steps against %d for an app with no workspace, so scoping dropped a step railpack generated", len(scoped.Steps), len(unscoped.Steps))
	}
}

func TestThePlanNeverCopiesWhatTheContextNoLongerCarries(t *testing.T) {
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

	plan := planned(t, root)

	for _, src := range plan.copied("install") {
		if strings.HasPrefix(filepath.ToSlash(src), "node_modules/") {
			t.Errorf("the install step copies %q, which the build context excludes, so the daemon cannot checksum it and the build fails before it starts", src)
		}
	}
}

func TestANextAppInAWorkspaceIsBuiltAndStartedAsTheAppRatherThanTheRoot(t *testing.T) {
	loc := located(t, "testdata/nextworkspace/apps/web")
	if !loc.InWorkspace() {
		t.Fatalf("Locate() = %+v, want the next app read as a member of the workspace above it", loc)
	}

	plan := plannedFrom(t, loc)

	if want := "pnpm --filter ./apps/web run start"; plan.Deploy.StartCommand != want {
		t.Errorf("the plan starts the app with %q, want %q — the root's start script serves nothing", plan.Deploy.StartCommand, want)
	}
	build := strings.Join(plan.step(t, "build"), "\n")
	if want := "pnpm --filter ./apps/web... run build"; !strings.Contains(build, want) {
		t.Errorf("the build step runs:\n%s\nwant %q", build, want)
	}
	install := strings.Join(plan.step(t, "install"), "\n")
	if want := "pnpm install --frozen-lockfile --filter ./apps/web..."; !strings.Contains(install, want) {
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

	var carried []string
	for _, input := range plan.Deploy.Inputs {
		if input.Step == "build" {
			carried = append(carried, input.Include...)
		}
	}
	if !contains(carried, ".") {
		t.Errorf("the deploy takes %v from the build, and none of it is the directory the app was built in: next writes apps/web/.next, and an image that carries only root-level paths starts without it", carried)
	}
}

const polyglotApp = "testdata/polyglotworkspace/apps/web"

func TestANodeAppIsPlannedAsOneEvenWhereAnotherLanguageSitsAtTheRoot(t *testing.T) {
	plan := planned(t, polyglotApp)

	install := strings.Join(plan.step(t, "install"), "\n")
	if want := "pnpm install --frozen-lockfile --filter ./apps/web..."; !strings.Contains(install, want) {
		t.Errorf("the install step runs:\n%s\nwant %q — the root carries a go.mod as well, and railpack takes the first language it detects", install, want)
	}
	if want := "pnpm --filter ./apps/web run start"; plan.Deploy.StartCommand != want {
		t.Errorf("the plan starts the app with %q, want %q", plan.Deploy.StartCommand, want)
	}
}

func TestTheRootsOwnRailpackFileStillShapesAPlanOcelForcesTheProviderOn(t *testing.T) {
	plan := planned(t, polyglotApp)

	apt := strings.Join(plan.step(t, "packages:apt:build"), "\n")
	if !strings.Contains(apt, "libvips-dev") {
		t.Errorf("the apt step runs:\n%s\nand the root's %s asks for libvips-dev: naming the provider replaced the file the user wrote rather than adding to it", apt, imagebuild.ConfigFileName)
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
