package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/credentials"
	"github.com/ocelhq/ocel/cli/internal/dotenv"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/lockfile"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provision"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// withCredentials points loadCredentials at apiURL for the duration of a test.
func withCredentials(t *testing.T, apiURL string) {
	t.Helper()
	prev := loadCredentials
	loadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{APIURL: apiURL, AccessToken: "tok"}, nil
	}
	t.Cleanup(func() { loadCredentials = prev })
}

// declareEnvScript is a discovery-path fixture that declares the given
// variable definitions (Connect JSON) via ResourceService.DeclareEnv, the way
// the SDK's defineEnv does.
func declareEnvScript(definitions ...string) string {
	return fmt.Sprintf(`
declare global {
  var __ocelRegister: Promise<unknown>[];
}
globalThis.__ocelRegister ??= [];
globalThis.__ocelRegister.push(
  fetch(new URL("/resources.v1.ResourceService/DeclareEnv", process.env.OCEL_DEV_SERVER), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ definitions: [%s] }),
  }),
);
export {};
`, strings.Join(definitions, ","))
}

// The dotfile is the file the developer edits, so it is the file that decides.
// A control-plane value that quietly outranked it would mean editing the file
// stopped working the day a teammate set one — the failure this feature exists
// to avoid. Resources stay above everything: those names are Ocel's own and
// can never collide with a declared variable.
func TestResolvedEnv_TheDotfileOutranksEveryOtherSourceButAResource(t *testing.T) {
	base := []string{"PATH=/bin", "CONTESTED=shell", "SHELL_ONLY=s"}
	projectEnv := map[string]string{"CONTESTED": "project"}
	live := map[string]string{"CONTESTED": "live"}
	dotfile := map[string]string{"CONTESTED": "dotfile", "DOTFILE_ONLY": "d"}
	resources := []provision.ProvisionedResource{
		{Name: "main", Env: map[string]string{"OCEL_RESOURCE_POSTGRES_main": "conn"}},
	}

	got := toMap(mergeEnv(base, projectEnv, live, dotfile, resources, ""))

	cases := map[string]string{
		"PATH":                        "/bin",
		"SHELL_ONLY":                  "s",
		"CONTESTED":                   "dotfile",
		"DOTFILE_ONLY":                "d",
		"OCEL_RESOURCE_POSTGRES_main": "conn",
	}
	for k, want := range cases {
		if got[k] != want {
			t.Errorf("env[%q] = %q, want %q", k, got[k], want)
		}
	}
}

// ocelhq-xd5j.34: every layer that runs user code has to be told which folder
// the app binds, or the SDK reports it binds the project root and refuses
// scoped reads whose values are in that very environment. The name is written
// unconditionally, so a binding left over in the developer's shell can never
// answer for this run.
func TestResolvedEnv_AlwaysStatesTheAppFolder(t *testing.T) {
	bound := resolvedEnv(nil, nil, nil, nil, "/web")
	if bound["OCEL_APP_FOLDER"] != "/web" {
		t.Errorf("OCEL_APP_FOLDER = %q, want %q", bound["OCEL_APP_FOLDER"], "/web")
	}

	unbound := resolvedEnv(nil, nil, nil, nil, "")
	folder, ok := unbound["OCEL_APP_FOLDER"]
	if !ok {
		t.Fatalf("resolvedEnv = %v, want OCEL_APP_FOLDER written even for an unbound app", unbound)
	}
	if folder != "" {
		t.Errorf("OCEL_APP_FOLDER = %q, want the project root spelled as the empty string", folder)
	}

	stale := toMap(mergeEnv([]string{"OCEL_APP_FOLDER=/stale"}, nil, nil, nil, nil, ""))
	if stale["OCEL_APP_FOLDER"] != "" {
		t.Errorf("OCEL_APP_FOLDER = %q, want the shell's stale binding overwritten", stale["OCEL_APP_FOLDER"])
	}

	// Last means last: no source dev merges may answer for the binding, or the
	// name the SDK reads stops being the one dev decided.
	contested := resolvedEnv(
		map[string]string{"OCEL_APP_FOLDER": "/from-project-env"},
		map[string]string{"OCEL_APP_FOLDER": "/from-live"},
		map[string]string{"OCEL_APP_FOLDER": "/from-dotfile"},
		[]provision.ProvisionedResource{{Name: "main", Env: map[string]string{"OCEL_APP_FOLDER": "/from-resource"}}},
		"/web",
	)
	if contested["OCEL_APP_FOLDER"] != "/web" {
		t.Errorf("OCEL_APP_FOLDER = %q, want the binding dev states to outrank every source it merges", contested["OCEL_APP_FOLDER"])
	}
}

// The remedy has to be one the developer can actually run. `ocel env set`
// needs a cloud provider and a bootstrapped store, and this whole path exists
// for the project that has neither — so dev's refusal names the file instead.
func TestDevRefusal_NamesTheDotfileRatherThanAStoreCommand(t *testing.T) {
	refusal := &envgate.Refusal{
		Problems: []*resourcesv1.VariableProblem{
			{Key: "DATABASE_URL", Kind: resourcesv1.VariableProblem_KIND_MISSING},
			{Key: "API_BASE", Folder: "/web", Kind: resourcesv1.VariableProblem_KIND_INVALID, Detail: "expected a URL"},
		},
		Scope: envgate.Scope{Apps: []envgate.App{{Name: "web", Folder: "/web"}}},
	}

	got := devRefusal(refusal, nil).Error()

	for _, want := range []string{
		"DATABASE_URL",
		"API_BASE",
		"/web",
		"no value is set",
		"expected a URL",
		"DATABASE_URL=<VALUE>",
		dotenv.FileName,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal = %q, want it to mention %q", got, want)
		}
	}
	if strings.Contains(got, "ocel env set") {
		t.Errorf("refusal = %q, want no `ocel env set`: it needs a cloud provider this path deliberately has none of", got)
	}
	if strings.Contains(got, "--preview") {
		t.Errorf("refusal = %q, want nothing about previews in a dev refusal", got)
	}
}

// A value exported in the shell is not a value dev resolves: the gate reads the
// file, so what it refuses on is versioned and the same for every developer.
// Saying only "no value is set" to someone looking at that very name in their
// own shell is the confusing half of that choice, so the refusal says which
// place it looked.
func TestDevRefusal_SaysSoWhenTheKeyIsOnlyInTheShell(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://from-the-shell")

	refusal := &envgate.Refusal{
		Problems: []*resourcesv1.VariableProblem{
			{Key: "DATABASE_URL", Kind: resourcesv1.VariableProblem_KIND_MISSING},
		},
	}

	got := devRefusal(refusal, nil).Error()

	if !strings.Contains(got, "shell") {
		t.Errorf("refusal = %q, want it to say the key was seen in the environment", got)
	}
	if strings.Contains(got, "postgres://from-the-shell") {
		t.Errorf("refusal = %q, want it to disclose no value", got)
	}

	// A key that is in the file and fails its schema is not a key dev looked in
	// the wrong place for, so the hint stays out of its way.
	inFile := devRefusal(refusal, dotfileKeySet(map[string]string{"DATABASE_URL": "postgres://from-the-file"})).Error()
	if strings.Contains(inFile, "set in this shell") {
		t.Errorf("refusal = %q, want no shell hint for a key the file does hold", inFile)
	}
}

// The refusal is rendered from the one file whose contents nothing else may
// see, and the obvious next edit to the schema branch is to show the offending
// value. There is none to show: what crosses the boundary is a key set, so the
// values are not in scope at the point the message is built.
func TestDevRefusal_IsNeverGivenAValueItCouldPrint(t *testing.T) {
	want := reflect.TypeOf(func(error, map[string]struct{}) error { return nil })
	if got := reflect.TypeOf(devRefusal); got != want {
		t.Fatalf("devRefusal is %s, want %s: any wider parameter puts a dotfile value in reach of the message", got, want)
	}

	refusal := &envgate.Refusal{
		Problems: []*resourcesv1.VariableProblem{
			{Key: "DATABASE_URL", Kind: resourcesv1.VariableProblem_KIND_MISSING},
			{Key: "API_TOKEN", Kind: resourcesv1.VariableProblem_KIND_INVALID, Detail: "expected a token"},
		},
	}
	dotfile := map[string]string{
		"DATABASE_URL": "postgres://must-not-appear",
		"API_TOKEN":    "sk-live-must-not-appear",
	}

	got := devRefusal(refusal, dotfileKeySet(dotfile)).Error()

	for _, value := range dotfile {
		if strings.Contains(got, value) {
			t.Errorf("refusal = %q, want it to disclose no value from %s", got, dotenv.FileName)
		}
	}
}

// The notice is what makes the divergence stated rather than discovered. It
// names keys only: this is the one file whose contents nothing else may see,
// so the notice about it cannot be the thing that prints them.
func TestReportDotfile_StatesWhatTheFileCostsAndPrintsNoValue(t *testing.T) {
	var quiet bytes.Buffer
	reportDotfile(&quiet, t.TempDir(), nil, nil, true)
	if quiet.Len() != 0 {
		t.Errorf("reportDotfile wrote %q for a run with no dotfile values, want nothing", quiet.String())
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	var out bytes.Buffer
	reportDotfile(&out, dir, map[string]string{"API_TOKEN": "sk-live-must-not-appear", "DATABASE_URL": "postgres://secret"}, nil, true)
	got := out.String()

	for _, want := range []string{"API_TOKEN", "DATABASE_URL", dotenv.FileName} {
		if !strings.Contains(got, want) {
			t.Errorf("notice = %q, want it to mention %q", got, want)
		}
	}
	for _, leaked := range []string{"sk-live", "postgres://secret"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("notice = %q, want it to disclose no value", got)
		}
	}
	// The two divergences a dotfile introduces, both currently stated nowhere.
	if !strings.Contains(got, "teammate") && !strings.Contains(got, "yours alone") {
		t.Errorf("notice = %q, want it to say the collaboration a shared store provides is gone", got)
	}
	if !strings.Contains(got, "plaintext") {
		t.Errorf("notice = %q, want it to say values reach the child in plaintext, which a deploy does not do", got)
	}
	if strings.Contains(got, ".gitignore") {
		t.Errorf("notice = %q, want no gitignore warning when the file is already ignored", got)
	}
}

// A notice that describes the run it is printed for. `ocel dev` watches the
// file, so telling its user to restart is false and would be read as "editing
// does nothing" — the surprise this notice exists to prevent. `ocel run` has
// no watcher, and the old sentence is simply true of it.
func TestReportDotfile_SaysWhetherThisRunWatchesTheFile(t *testing.T) {
	values := map[string]string{"API_TOKEN": "x"}

	var watched bytes.Buffer
	reportDotfile(&watched, t.TempDir(), values, nil, true)
	if got := watched.String(); !strings.Contains(got, "re-resolves this run") {
		t.Errorf("notice = %q, want it to say an edit re-resolves the running `ocel dev`", got)
	}
	if got := watched.String(); strings.Contains(got, "read once, at startup") {
		t.Errorf("notice = %q, want no claim that the file is held for the process", got)
	}

	var once bytes.Buffer
	reportDotfile(&once, t.TempDir(), values, nil, false)
	if got := once.String(); !strings.Contains(got, "read once, at startup") {
		t.Errorf("notice = %q, want a run with no watcher to still say the file is read once", got)
	}
}

// `ocel init` scaffolds no .gitignore, and this feature's whole purpose is to
// encourage putting values — including secrets — in that file. An unignored one
// is how a secret reaches a public repository.
func TestReportDotfile_WarnsWhenTheFileIsNotIgnored(t *testing.T) {
	var out bytes.Buffer
	reportDotfile(&out, t.TempDir(), map[string]string{"API_TOKEN": "x"}, nil, true)

	if got := out.String(); !strings.Contains(got, ".gitignore") {
		t.Errorf("notice = %q, want it to say the file is not ignored by git", got)
	}

	// A re-inclusion is the last word git gives it, so it is the last word here:
	// staying quiet about a file git will happily commit is the one direction
	// this check must not be wrong in.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env*\n!.env\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	var reincluded bytes.Buffer
	reportDotfile(&reincluded, dir, map[string]string{"API_TOKEN": "x"}, nil, true)
	if got := reincluded.String(); !strings.Contains(got, ".gitignore") {
		t.Errorf("notice = %q, want the warning when a later line re-includes the file", got)
	}
}

// End to end: the file the developer edits reaches the process the developer
// runs, under the key's own name, and the app is told the folder it binds so a
// scoped read resolves instead of throwing.
func TestRunDev_DeliversTheDotfileAndTheAppFolderIntoTheChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	resolveServer := newFakeResolveServer(t)
	defer resolveServer.Close()

	root := t.TempDir()
	t.Cleanup(func() { _ = lockfile.Remove(root) })

	withCredentials(t, resolveServer.URL)
	writeLink(t, root, resolveServer.URL, "proj_"+t.Name())
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app", apps: [{ name: "web", path: "apps/web", folder: "/web" }] };
`)
	// The lines around it are the ones a pre-existing Next project's .env
	// already holds. None of them is Ocel's, and none of them may stop the run.
	writeFile(t, filepath.Join(root, ".env"), "NEXT_PUBLIC_SITE_URL=https://example.com\nAWS_PROFILE=dev\napi_base=lower\nAPI_BASE=http://localhost:3000\nnot an assignment\n")
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"API_BASE","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}`))

	envDumpPath := filepath.Join(root, "env.out")
	appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

	var stdout, stderr syncBuffer
	err := runDev(context.Background(), nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("runDev err = %v, want exit 7; stderr=%s", err, stderr.String())
	}

	dumped, readErr := os.ReadFile(envDumpPath)
	if readErr != nil {
		t.Fatalf("read env dump: %v", readErr)
	}
	env := toMap(strings.Split(strings.TrimRight(string(dumped), "\n"), "\n"))

	if env["API_BASE"] != "http://localhost:3000" {
		t.Errorf("API_BASE = %q, want the dotfile's value", env["API_BASE"])
	}
	if env["OCEL_APP_FOLDER"] != "/web" {
		t.Errorf("OCEL_APP_FOLDER = %q, want the folder the only app binds", env["OCEL_APP_FOLDER"])
	}
	if !strings.Contains(stdout.String(), "API_BASE") {
		t.Errorf("stdout = %q, want the divergence notice to name the key", stdout.String())
	}
	if !strings.Contains(stdout.String(), "line 5") {
		t.Errorf("stdout = %q, want the line that assigns nothing reported by number", stdout.String())
	}
	if strings.Contains(stdout.String(), "NEXT_PUBLIC_SITE_URL") {
		t.Errorf("stdout = %q, want a line Ocel does not own passed over in silence", stdout.String())
	}
}

// The gate refuses before anything is spawned, so a missing value is a named
// failure at startup rather than a crash inside the app.
func TestRunDev_RefusesWhenTheDotfileDoesNotHoldARequiredValue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	resolveServer := newFakeResolveServer(t)
	defer resolveServer.Close()

	root := t.TempDir()
	t.Cleanup(func() { _ = lockfile.Remove(root) })

	withCredentials(t, resolveServer.URL)
	writeLink(t, root, resolveServer.URL, "proj_"+t.Name())
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"DATABASE_URL","class":"VARIABLE_CLASS_PLAIN","required":true}`))

	startedPath := filepath.Join(root, "started")
	appCmd := []string{"sh", "-c", "touch " + startedPath}

	var stdout, stderr syncBuffer
	err := runDev(context.Background(), nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

	if err == nil {
		t.Fatal("runDev = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), dotenv.FileName) {
		t.Errorf("err = %q, want it to name DATABASE_URL and %s", err.Error(), dotenv.FileName)
	}
	if _, statErr := os.Stat(startedPath); statErr == nil {
		t.Error("the app was started despite the refusal")
	}
}

// The gate rules per app, over each app's own binding; dev states one binding
// for the whole project. Where those cannot be the same answer, a green gate
// would be followed by an EnvScopeError at the first read — the crash the gate
// exists to replace. It refuses instead, and never with a remedy that needs dev
// to know which app it is running.
func TestCheckStatableBinding_RefusesWhenTheAppsDoNotAgreeOnOne(t *testing.T) {
	apps := []projectconfig.App{
		{Name: "web", Path: "apps/web", Folder: "/web"},
		{Name: "api", Path: "apps/api", Folder: "/api"},
	}

	if err := checkStatableBinding(apps, "", nil); err != nil {
		t.Errorf("checkStatableBinding = %v, want nil with no scoped variable declared", err)
	}

	agreed := []projectconfig.App{{Name: "web", Folder: "/web"}, {Name: "admin", Folder: "/web"}}
	if err := checkStatableBinding(agreed, "/web", map[string][]string{"API_BASE": {"/web"}}); err != nil {
		t.Errorf("checkStatableBinding = %v, want nil when every app binds the folder dev states", err)
	}

	err := checkStatableBinding(apps, "", map[string][]string{"API_BASE": {"/web", "/api"}})
	if err == nil {
		t.Fatal("checkStatableBinding = nil, want a refusal: no child of this run could read API_BASE")
	}
	got := err.Error()
	for _, want := range []string{"API_BASE", "web binds /web", "api binds /api", "the project root", dotenv.FileName, "ocel.config.ts"} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal = %q, want it to mention %q", got, want)
		}
	}
	if strings.Contains(got, "one `ocel dev` per app") || strings.Contains(got, "per app") {
		t.Errorf("refusal = %q, want no per-app remedy: election keys on the project root, so a second run is a follower", got)
	}
}

// A key scoped to a folder no app binds is unreadable under every app's own
// binding too, so a deploy of the same project is just as silent about it. Dev
// costs that project nothing by starting, and refusing it would refuse a
// project that deploys.
func TestCheckStatableBinding_StartsWhenNoAppBindsTheKeysScope(t *testing.T) {
	apps := []projectconfig.App{
		{Name: "web", Path: "apps/web", Folder: "/web"},
		{Name: "api", Path: "apps/api", Folder: "/api"},
	}

	if err := checkStatableBinding(apps, "", map[string][]string{"NOBODY": {"/nowhere"}}); err != nil {
		t.Errorf("checkStatableBinding = %v, want nil: no app binds /nowhere, so no read is lost", err)
	}

	scoped := map[string][]string{"NOBODY": {"/nowhere"}, "API_BASE": {"/web"}}
	err := checkStatableBinding(apps, "", scoped)
	if err == nil {
		t.Fatal("checkStatableBinding = nil, want a refusal for API_BASE, which web would read under its own binding")
	}
	if strings.Contains(err.Error(), "NOBODY") {
		t.Errorf("refusal = %q, want it silent about NOBODY: naming a key no app binds sends the developer after nothing", err.Error())
	}
}

// The refusal is a list of what this run cannot do, so it names only the apps
// that lose a read: an app bound outside the key's scope was never going to
// read it, and listing it makes the remedy read as if it were about them.
func TestCheckStatableBinding_NamesOnlyTheAppsBindingTheKeysScope(t *testing.T) {
	apps := []projectconfig.App{
		{Name: "web", Path: "apps/web", Folder: "/web"},
		{Name: "api", Path: "apps/api", Folder: "/api"},
	}

	err := checkStatableBinding(apps, "", map[string][]string{"API_BASE": {"/web"}})
	if err == nil {
		t.Fatal("checkStatableBinding = nil, want a refusal: web would read API_BASE under its own binding")
	}
	got := err.Error()
	if !strings.Contains(got, "API_BASE") || !strings.Contains(got, "web binds /web") {
		t.Errorf("refusal = %q, want it to name API_BASE and web's binding", got)
	}
	if strings.Contains(got, "api binds /api") {
		t.Errorf("refusal = %q, want no mention of api: /api is not in API_BASE's scope", got)
	}
}

// A project can lose more than one read at once, and the refusal is the whole
// list: every losing key, in a fixed order, and every app that loses one. Go
// randomises map iteration, so without the sort the same broken project would
// print a different refusal on every run — the message is asserted whole, and
// repeatedly, because a text that varies per run is not a text a developer can
// compare against a teammate's.
func TestCheckStatableBinding_ListsEveryLosingKeyAndAppInAFixedOrder(t *testing.T) {
	apps := []projectconfig.App{
		{Name: "web", Path: "apps/web", Folder: "/web"},
		{Name: "api", Path: "apps/api", Folder: "/api"},
	}
	scoped := map[string][]string{
		"D_KEY": {"/api"},
		"B_KEY": {"/api"},
		"C_KEY": {"/web"},
		"A_KEY": {"/web"},
	}

	want := "A_KEY, B_KEY, C_KEY, D_KEY are scoped to a folder this run cannot state — the app has not been started.\n" +
		"\n  web binds /web\n  api binds /api\n\n" +
		"`ocel dev` and `ocel run` spawn one child for the whole project and nothing tells it which app that child is, " +
		"so the binding they state is the project root. A scoped read refuses under it, even with the value in " + dotenv.FileName + ".\n\n" +
		"fix: bind every app to the same folder in ocel.config.ts, or drop `folders:` from those declarations."

	for range 50 {
		err := checkStatableBinding(apps, "", scoped)
		if err == nil {
			t.Fatal("checkStatableBinding = nil, want a refusal: both apps lose a read")
		}
		if got := err.Error(); got != want {
			t.Fatalf("refusal =\n%q\nwant\n%q", got, want)
		}
	}
}

// The gate reporting ready for a read the child provably cannot make is the one
// failure the gate was put in dev to prevent, so the refusal is end to end.
func TestRunDev_RefusesAScopedVariableNoChildOfTheRunCouldRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	resolveServer := newFakeResolveServer(t)
	defer resolveServer.Close()

	root := t.TempDir()
	t.Cleanup(func() { _ = lockfile.Remove(root) })

	withCredentials(t, resolveServer.URL)
	writeLink(t, root, resolveServer.URL, "proj_"+t.Name())
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app", apps: [{ name: "web", path: "apps/web", folder: "/web" }, { name: "api", path: "apps/api", folder: "/api" }] };
`)
	writeFile(t, filepath.Join(root, ".env"), "API_BASE=http://localhost:3000\n")
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"API_BASE","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}`))

	startedPath := filepath.Join(root, "started")
	appCmd := []string{"sh", "-c", "touch " + startedPath}

	var stdout, stderr syncBuffer
	err := runDev(context.Background(), nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

	if err == nil {
		t.Fatal("runDev = nil, want a refusal rather than a green gate and a throw at the first read")
	}
	if !strings.Contains(err.Error(), "API_BASE") {
		t.Errorf("err = %q, want it to name the scoped key", err.Error())
	}
	if _, statErr := os.Stat(startedPath); statErr == nil {
		t.Error("the app was started despite the refusal")
	}
}

// A deploy of this project succeeds and says nothing about NOBODY: every app's
// own binding is outside its scope, so no app resolves it there either. Dev
// must not be stricter than the deploy it stands in for, so the run starts.
//
// The value is in the file on purpose — a required scoped key with none is
// refused by the gate itself, one step earlier, and this test would then pass
// without ever reaching the binding check.
func TestRunDev_StartsWhenAScopedVariableIsBoundByNoApp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	resolveServer := newFakeResolveServer(t)
	defer resolveServer.Close()

	root := t.TempDir()
	t.Cleanup(func() { _ = lockfile.Remove(root) })

	withCredentials(t, resolveServer.URL)
	writeLink(t, root, resolveServer.URL, "proj_"+t.Name())
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app", apps: [{ name: "web", path: "apps/web", folder: "/web" }, { name: "api", path: "apps/api", folder: "/api" }] };
`)
	writeFile(t, filepath.Join(root, ".env"), "NOBODY=x\n")
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"NOBODY","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/nowhere"]}`))

	startedPath := filepath.Join(root, "started")
	appCmd := []string{"sh", "-c", "touch " + startedPath + "; exit 7"}

	var stdout, stderr syncBuffer
	err := runDev(context.Background(), nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("runDev err = %v, want the app to have run and exited 7; stderr=%s", err, stderr.String())
	}
	if _, statErr := os.Stat(startedPath); statErr != nil {
		t.Errorf("the app was not started: %v", statErr)
	}
}

// `ocel run` gates the same project the same way `ocel dev` does, so the
// narrowing has to reach it too — they share one discovery path and this is
// what says so.
func TestRunRun_StartsWhenAScopedVariableIsBoundByNoApp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	resolveServer := newFakeResolveServer(t)
	defer resolveServer.Close()

	root := t.TempDir()
	t.Cleanup(func() { _ = lockfile.Remove(root) })

	withCredentials(t, resolveServer.URL)
	writeLink(t, root, resolveServer.URL, "proj_"+t.Name())
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app", apps: [{ name: "web", path: "apps/web", folder: "/web" }, { name: "api", path: "apps/api", folder: "/api" }] };
`)
	writeFile(t, filepath.Join(root, ".env"), "NOBODY=x\n")
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"NOBODY","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/nowhere"]}`))

	startedPath := filepath.Join(root, "started")
	appCmd := []string{"sh", "-c", "touch " + startedPath + "; exit 7"}

	var stdout, stderr bytes.Buffer
	err := runRun(context.Background(), nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("runRun err = %v, want the app to have run and exited 7; stderr=%s", err, stderr.String())
	}
	if _, statErr := os.Stat(startedPath); statErr != nil {
		t.Errorf("the app was not started: %v", statErr)
	}
}

// `ocel run` runs the project's own code, so it resolves and gates the file the
// same way `ocel dev` does. Answering differently meant a project set up to run
// under dev failed under run, with the `ocel env set` remedy dev's refusal
// exists to avoid.
func TestRunRun_ResolvesTheDotfileAndGatesLikeDev(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	resolveServer := newFakeResolveServer(t)
	defer resolveServer.Close()

	root := t.TempDir()
	t.Cleanup(func() { _ = lockfile.Remove(root) })

	withCredentials(t, resolveServer.URL)
	writeLink(t, root, resolveServer.URL, "proj_"+t.Name())
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app", apps: [{ name: "web", path: "apps/web", folder: "/web" }] };
`)
	writeFile(t, filepath.Join(root, ".env"), "API_BASE=http://localhost:3000\n")
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(
		`{"key":"API_BASE","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}`))

	envDumpPath := filepath.Join(root, "env.out")
	appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

	var stdout, stderr bytes.Buffer
	err := runRun(context.Background(), nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("runRun err = %v, want exit 7; stderr=%s", err, stderr.String())
	}

	dumped, readErr := os.ReadFile(envDumpPath)
	if readErr != nil {
		t.Fatalf("read env dump: %v", readErr)
	}
	env := toMap(strings.Split(strings.TrimRight(string(dumped), "\n"), "\n"))
	if env["API_BASE"] != "http://localhost:3000" {
		t.Errorf("API_BASE = %q, want `ocel run` to resolve the dotfile the way `ocel dev` does", env["API_BASE"])
	}
	if env["OCEL_APP_FOLDER"] != "/web" {
		t.Errorf("OCEL_APP_FOLDER = %q, want the folder the only app binds", env["OCEL_APP_FOLDER"])
	}
}

// The gate has to answer the same way under `ocel run` as under `ocel dev`, or
// the same project is refused by one and started by the other.
func TestRunRun_RefusesWhenTheDotfileDoesNotHoldARequiredValue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	resolveServer := newFakeResolveServer(t)
	defer resolveServer.Close()

	root := t.TempDir()
	t.Cleanup(func() { _ = lockfile.Remove(root) })

	withCredentials(t, resolveServer.URL)
	writeLink(t, root, resolveServer.URL, "proj_"+t.Name())
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"DATABASE_URL","class":"VARIABLE_CLASS_PLAIN","required":true}`))

	startedPath := filepath.Join(root, "started")
	appCmd := []string{"sh", "-c", "touch " + startedPath}

	var stdout, stderr bytes.Buffer
	err := runRun(context.Background(), nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

	if err == nil {
		t.Fatal("runRun = nil, want the same refusal `ocel dev` gives")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), dotenv.FileName) {
		t.Errorf("err = %q, want it to name DATABASE_URL and %s", err.Error(), dotenv.FileName)
	}
	if strings.Contains(err.Error(), "ocel env set") {
		t.Errorf("err = %q, want no `ocel env set`: it needs the cloud account this path does without", err.Error())
	}
	if _, statErr := os.Stat(startedPath); statErr == nil {
		t.Error("the command was started despite the refusal")
	}
}

// A line the parser could not read is neither Ocel's nor the framework's, so it
// is reported by number — and only by number, since a line that assigns nothing
// is the shape a pasted token has.
func TestReportDotfile_NamesTheUnreadableLinesByNumberOnly(t *testing.T) {
	var out bytes.Buffer
	reportDotfile(&out, t.TempDir(), nil, []int{2, 5}, true)
	got := out.String()

	for _, want := range []string{dotenv.FileName, "2, 5"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice = %q, want it to mention %q", got, want)
		}
	}

	var one bytes.Buffer
	reportDotfile(&one, t.TempDir(), nil, []int{4}, true)
	if !strings.Contains(one.String(), "line 4 is") {
		t.Errorf("notice = %q, want a singular line reported singularly", one.String())
	}
}

// withProjectEnv points the control-plane fetch at envVars for the duration of
// a test and reports how many times the run asked for it.
func withProjectEnv(t *testing.T, envVars map[string]string) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	prev := fetchProjectConfig
	fetchProjectConfig = func(_ context.Context, apiURL, token, projectID string) (provision.ProjectConfig, error) {
		calls.Add(1)
		return provision.ProjectConfig{ProjectID: projectID, EnvVars: envVars, APIURL: apiURL, Token: token}, nil
	}
	t.Cleanup(func() { fetchProjectConfig = prev })
	return &calls
}

// The control plane is one of the three sources a run delivers, so it is one of
// the three the gate rules from. A team keeping its values in the shared store
// must not be told to duplicate them into a file that is one machine's.
func TestRunDev_AControlPlaneValueSatisfiesTheGateWithoutADotfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	resolveServer := newFakeResolveServer(t)
	defer resolveServer.Close()

	root := t.TempDir()
	t.Cleanup(func() { _ = lockfile.Remove(root) })

	withCredentials(t, resolveServer.URL)
	calls := withProjectEnv(t, map[string]string{"STRIPE_API_KEY": "sk_from_store"})
	writeLink(t, root, resolveServer.URL, "proj_"+t.Name())
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}`))

	envDumpPath := filepath.Join(root, "env.out")
	appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

	var stdout, stderr syncBuffer
	err := runDev(context.Background(), nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("runDev err = %v, want exit 7 (no refusal); stderr=%s", err, stderr.String())
	}

	dumped, readErr := os.ReadFile(envDumpPath)
	if readErr != nil {
		t.Fatalf("read env dump: %v", readErr)
	}
	env := toMap(strings.Split(strings.TrimRight(string(dumped), "\n"), "\n"))
	if env["STRIPE_API_KEY"] != "sk_from_store" {
		t.Errorf("STRIPE_API_KEY = %q, want the control plane's value", env["STRIPE_API_KEY"])
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("project config fetched %d times, want exactly 1 for the run", got)
	}
}

// The gate now sees both sources, so the precedence between them has to hold at
// the gate too: the file the developer edits still decides.
func TestRunDev_TheDotfileStillOutranksTheControlPlaneAtTheGate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	resolveServer := newFakeResolveServer(t)
	defer resolveServer.Close()

	root := t.TempDir()
	t.Cleanup(func() { _ = lockfile.Remove(root) })

	withCredentials(t, resolveServer.URL)
	withProjectEnv(t, map[string]string{"STRIPE_API_KEY": "sk_from_store"})
	writeLink(t, root, resolveServer.URL, "proj_"+t.Name())
	writeFile(t, filepath.Join(root, ".env"), "STRIPE_API_KEY=sk_from_file\n")
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}`))

	envDumpPath := filepath.Join(root, "env.out")
	appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

	var stdout, stderr syncBuffer
	err := runDev(context.Background(), nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("runDev err = %v, want exit 7; stderr=%s", err, stderr.String())
	}

	dumped, readErr := os.ReadFile(envDumpPath)
	if readErr != nil {
		t.Fatalf("read env dump: %v", readErr)
	}
	env := toMap(strings.Split(strings.TrimRight(string(dumped), "\n"), "\n"))
	if env["STRIPE_API_KEY"] != "sk_from_file" {
		t.Errorf("STRIPE_API_KEY = %q, want the dotfile's value", env["STRIPE_API_KEY"])
	}
}

// Getting started needs no cloud account, so an unreachable control plane costs
// the run its shared values and nothing else: it says what it lost and gates
// from the file alone.
func TestRunDev_FallsBackToTheDotfileWhenTheControlPlaneIsUnreachable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	resolveServer := newFakeResolveServer(t)
	defer resolveServer.Close()

	root := t.TempDir()
	t.Cleanup(func() { _ = lockfile.Remove(root) })

	withCredentials(t, resolveServer.URL)
	prev := fetchProjectConfig
	fetchProjectConfig = func(context.Context, string, string, string) (provision.ProjectConfig, error) {
		return provision.ProjectConfig{}, errors.New("dial tcp: connection refused")
	}
	t.Cleanup(func() { fetchProjectConfig = prev })

	writeLink(t, root, resolveServer.URL, "proj_"+t.Name())
	writeFile(t, filepath.Join(root, ".env"), "API_BASE=http://localhost:3000\n")
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"API_BASE","class":"VARIABLE_CLASS_PLAIN","required":true}`))

	envDumpPath := filepath.Join(root, "env.out")
	appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

	var stdout, stderr syncBuffer
	err := runDev(context.Background(), nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("runDev err = %v, want exit 7 (offline is not a failure); stderr=%s", err, stderr.String())
	}

	dumped, readErr := os.ReadFile(envDumpPath)
	if readErr != nil {
		t.Fatalf("read env dump: %v", readErr)
	}
	env := toMap(strings.Split(strings.TrimRight(string(dumped), "\n"), "\n"))
	if env["API_BASE"] != "http://localhost:3000" {
		t.Errorf("API_BASE = %q, want the dotfile's value", env["API_BASE"])
	}

	notice := stdout.String() + stderr.String()
	if !strings.Contains(notice, dotenv.FileName) || !strings.Contains(notice, "connection refused") {
		t.Errorf("output = %q, want a warning naming what was unreachable and that only %s is in play", notice, dotenv.FileName)
	}
}

// `ocel run` shares the gate, so it shares the sources the gate rules from.
func TestRunRun_AControlPlaneValueSatisfiesTheGateWithoutADotfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	resolveServer := newFakeResolveServer(t)
	defer resolveServer.Close()

	root := t.TempDir()
	t.Cleanup(func() { _ = lockfile.Remove(root) })

	withCredentials(t, resolveServer.URL)
	withProjectEnv(t, map[string]string{"STRIPE_API_KEY": "sk_from_store"})
	writeLink(t, root, resolveServer.URL, "proj_"+t.Name())
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}`))

	envDumpPath := filepath.Join(root, "env.out")
	appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

	var stdout, stderr syncBuffer
	err := runRun(context.Background(), nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("runRun err = %v, want exit 7 (no refusal); stderr=%s", err, stderr.String())
	}

	dumped, readErr := os.ReadFile(envDumpPath)
	if readErr != nil {
		t.Fatalf("read env dump: %v", readErr)
	}
	env := toMap(strings.Split(strings.TrimRight(string(dumped), "\n"), "\n"))
	if env["STRIPE_API_KEY"] != "sk_from_store" {
		t.Errorf("STRIPE_API_KEY = %q, want the control plane's value", env["STRIPE_API_KEY"])
	}
}

// A live-class key's source is keyed on the keys the run declared, so it can
// only be read after discovery — after the gate has ruled. Refusing for its
// absence would refuse every project that has one, so dev exempts it and says
// what it did instead: the value is resolved once, at sync.
func TestRunDev_ALiveClassKeyIsNotRefusedForHavingNoLocalValue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	resolveServer := newFakeResolveServer(t)
	defer resolveServer.Close()

	root := t.TempDir()
	t.Cleanup(func() { _ = lockfile.Remove(root) })

	withCredentials(t, resolveServer.URL)
	writeLink(t, root, resolveServer.URL, "proj_"+t.Name())
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"DB_PASSWORD","class":"VARIABLE_CLASS_SECRET","required":true}`))

	startedPath := filepath.Join(root, "started")
	appCmd := []string{"sh", "-c", "touch " + startedPath + "; exit 7"}

	var stdout, stderr syncBuffer
	err := runDev(context.Background(), nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("runDev err = %v, want exit 7 (no refusal); stderr=%s", err, stderr.String())
	}
	if _, statErr := os.Stat(startedPath); statErr != nil {
		t.Fatalf("the app was not started: %v", statErr)
	}
	if !strings.Contains(stdout.String(), "DB_PASSWORD") {
		t.Errorf("stdout = %q, want the live-value notice to name DB_PASSWORD", stdout.String())
	}
}
