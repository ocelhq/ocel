package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/devlock"
	"github.com/ocelhq/ocel/cli/internal/dotenv"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/resolve"
	"github.com/ocelhq/ocel/pkg/channel"
	"github.com/ocelhq/ocel/pkg/constants"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func declareEnvScript(definitions ...string) string {
	return fmt.Sprintf(`
declare global {
  var __ocelRegister: Promise<unknown>[];
}
globalThis.__ocelRegister ??= [];
globalThis.__ocelRegister.push(
  fetch(new URL("/app.resources.v1.ResourceService/DeclareEnv", process.env.%s), {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + process.env.`+constants.DevServerTokenEnvName+` },
    body: JSON.stringify({ definitions: [%s] }),
  }),
);
export {};
`, constants.DevServerEnvName, strings.Join(definitions, ","))
}

func TestResolvedEnv(t *testing.T) {
	t.Parallel()

	t.Run("the dev values outrank every other source but a resource", func(t *testing.T) {
		t.Parallel()

		base := []string{"PATH=/bin", "CONTESTED=shell", "SHELL_ONLY=s"}
		live := map[string]string{"CONTESTED": "live"}
		dotfile := map[string]string{"CONTESTED": "dotfile", "DOTFILE_ONLY": "d"}
		resources := []resolve.Resource{
			{Name: "main", Env: map[string]string{"OCEL_RESOURCE_POSTGRES_main": "conn"}},
		}

		got := toMap(mergeEnv(base, live, dotfile, resources, runtimeAccess{}, "", envgate.Scope{}))

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
	})

	t.Run("live values are delivered at startup", func(t *testing.T) {
		t.Parallel()

		values := map[string]string{"VALUE_ONLY": "v"}
		live := map[string]string{"WEBHOOK_SECRET": "whsec_live"}
		resources := []resolve.Resource{
			{Name: "main", Env: map[string]string{"OCEL_RESOURCE_POSTGRES_main": "conn"}},
		}

		got := resolvedEnv(live, values, resources, runtimeAccess{}, "", envgate.Scope{})

		cases := map[string]string{
			"VALUE_ONLY":                  "v",
			"WEBHOOK_SECRET":              "whsec_live",
			"OCEL_RESOURCE_POSTGRES_main": "conn",
		}
		for k, want := range cases {
			if got[k] != want {
				t.Errorf("env[%q] = %q, want %q", k, got[k], want)
			}
		}
	})

	t.Run("the runtime address travels with the token its routes answer to, and neither alone", func(t *testing.T) {
		t.Parallel()

		reached := resolvedEnv(nil, nil, nil, runtimeAccess{address: "http://127.0.0.1:4242", token: "app-token"}, "", envgate.Scope{})
		if reached[constants.RuntimeAddressEnvName] != "http://127.0.0.1:4242" || reached[channel.SessionTokenEnvVar] != "app-token" {
			t.Errorf("env = %v, want %s and %s stated together", reached, constants.RuntimeAddressEnvName, channel.SessionTokenEnvVar)
		}

		unreached := resolvedEnv(nil, nil, nil, runtimeAccess{}, "", envgate.Scope{})
		for _, name := range []string{constants.RuntimeAddressEnvName, channel.SessionTokenEnvVar} {
			if _, ok := unreached[name]; ok {
				t.Errorf("%s stated for an app with no runtime to reach", name)
			}
		}
	})

	t.Run("the app folder is always stated", func(t *testing.T) {
		t.Parallel()

		bound := resolvedEnv(nil, nil, nil, runtimeAccess{}, "/web", envgate.Scope{})
		if bound[constants.AppFolderEnvName] != "/web" {
			t.Errorf("%s = %q, want %q", constants.AppFolderEnvName, bound[constants.AppFolderEnvName], "/web")
		}

		unbound := resolvedEnv(nil, nil, nil, runtimeAccess{}, "", envgate.Scope{})
		folder, ok := unbound[constants.AppFolderEnvName]
		if !ok {
			t.Fatalf("resolvedEnv = %v, want %s written even for an unbound app", unbound, constants.AppFolderEnvName)
		}
		if folder != "" {
			t.Errorf("%s = %q, want the project root spelled as the empty string", constants.AppFolderEnvName, folder)
		}

		stale := toMap(mergeEnv([]string{constants.AppFolderEnvName + "=/stale"}, nil, nil, nil, runtimeAccess{}, "", envgate.Scope{}))
		if stale[constants.AppFolderEnvName] != "" {
			t.Errorf("%s = %q, want the shell's stale binding overwritten", constants.AppFolderEnvName, stale[constants.AppFolderEnvName])
		}

		contested := resolvedEnv(
			map[string]string{constants.AppFolderEnvName: "/from-live"},
			map[string]string{constants.AppFolderEnvName: "/from-dotfile"},
			[]resolve.Resource{{Name: "main", Env: map[string]string{constants.AppFolderEnvName: "/from-resource"}}},
			runtimeAccess{},
			"/web",
			envgate.Scope{},
		)
		if contested[constants.AppFolderEnvName] != "/web" {
			t.Errorf("%s = %q, want the binding dev states to outrank every source it merges", constants.AppFolderEnvName, contested[constants.AppFolderEnvName])
		}
	})
}

func TestDevRefusal(t *testing.T) {
	t.Run("it names the dotfile rather than a store command", func(t *testing.T) {
		refusal := &envgate.Refusal{
			Problems: []*resourcesv1.VariableProblem{
				{Key: "DATABASE_URL", Kind: resourcesv1.VariableProblem_KIND_MISSING},
				{Key: "API_BASE", Folder: "/web", Kind: resourcesv1.VariableProblem_KIND_INVALID, Detail: "expected a URL"},
			},
			Scope: envgate.Scope{Apps: []envgate.App{{Name: "web", Folder: "/web"}}},
		}

		got := devRefusal(refusal, nil, invocation{name: "dev", source: devSource{id: "dotenv"}}).Error()

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
	})

	t.Run("under another dev source it names that source and "+dotenv.LocalFileName, func(t *testing.T) {
		refusal := &envgate.Refusal{Problems: []*resourcesv1.VariableProblem{{Key: "DATABASE_URL", Kind: resourcesv1.VariableProblem_KIND_MISSING}}}

		got := devRefusal(refusal, nil, invocation{name: "dev", source: devSource{id: "infisical:p-1/dev", values: map[string]string{}}}).Error()

		for _, want := range []string{"set DATABASE_URL in infisical:p-1/dev", "DATABASE_URL=<VALUE> to " + dotenv.LocalFileName, "Set the values above in infisical:p-1/dev and " + dotenv.LocalFileName} {
			if !strings.Contains(got, want) {
				t.Errorf("refusal = %q, want it to say %q", got, want)
			}
		}
	})

	t.Run("it says so when the key is only in the shell", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://from-the-shell")

		refusal := &envgate.Refusal{
			Problems: []*resourcesv1.VariableProblem{
				{Key: "DATABASE_URL", Kind: resourcesv1.VariableProblem_KIND_MISSING},
			},
		}

		got := devRefusal(refusal, nil, invocation{name: "dev", source: devSource{id: "dotenv"}}).Error()

		if !strings.Contains(got, "shell") {
			t.Errorf("refusal = %q, want it to say the key was seen in the environment", got)
		}
		if strings.Contains(got, "postgres://from-the-shell") {
			t.Errorf("refusal = %q, want it to disclose no value", got)
		}

		inFile := devRefusal(refusal, dotfileValues(map[string]string{"DATABASE_URL": "postgres://from-the-file"}).keys(), invocation{name: "dev", source: devSource{id: "dotenv"}}).Error()
		if strings.Contains(inFile, "set in this shell") {
			t.Errorf("refusal = %q, want no shell hint for a key the file does hold", inFile)
		}
	})

	t.Run("it is never given a value it could print", func(t *testing.T) {
		want := reflect.TypeOf(func(error, map[string]struct{}, invocation) error { return nil })
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

		got := devRefusal(refusal, dotfileValues(dotfile).keys(), invocation{name: "dev", source: devSource{id: "dotenv"}}).Error()

		for _, value := range dotfile {
			if strings.Contains(got, value) {
				t.Errorf("refusal = %q, want it to disclose no value from %s", got, dotenv.FileName)
			}
		}
	})
}

func TestRefusalsNameTheCommandThatRan(t *testing.T) {
	refusal := &envgate.Refusal{
		Problems: []*resourcesv1.VariableProblem{
			{Key: "DATABASE_URL", Kind: resourcesv1.VariableProblem_KIND_MISSING},
		},
	}

	t.Run("a variable refusal names the command that was run", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://from-the-shell")

		got := devRefusal(refusal, nil, invocation{name: "run", source: devSource{id: "dotenv"}}).Error()

		if !strings.Contains(got, "`ocel run` again") {
			t.Errorf("refusal = %q, want it to name the command that was run", got)
		}
		if strings.Contains(got, "`ocel dev`") {
			t.Errorf("refusal = %q, want no mention of a command or flag this run never used", got)
		}
		if strings.Contains(got, "ocel login") {
			t.Errorf("refusal = %q, want no login hint for a run that was not logged out of a linked project", got)
		}
	})
}

func dotfileValues(values map[string]string) devValues {
	return devValues{{from: dotenv.FileName, file: true, values: values}}
}

func TestReportDevValues(t *testing.T) {
	t.Parallel()

	t.Run("it states what the file costs and prints no value", func(t *testing.T) {
		t.Parallel()

		var quiet bytes.Buffer
		reportDevValues(&quiet, t.TempDir(), dotfileValues(nil), true)
		if quiet.Len() != 0 {
			t.Errorf("reportDevValues wrote %q for a run with no dotfile values, want nothing", quiet.String())
		}

		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
			t.Fatalf("write .gitignore: %v", err)
		}

		var out bytes.Buffer
		reportDevValues(&out, dir, dotfileValues(map[string]string{"API_TOKEN": "sk-live-must-not-appear", "DATABASE_URL": "postgres://secret"}), true)
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
		if !strings.Contains(got, "teammate") && !strings.Contains(got, "yours alone") {
			t.Errorf("notice = %q, want it to say the collaboration a shared store provides is gone", got)
		}
		if !strings.Contains(got, "plaintext") {
			t.Errorf("notice = %q, want it to say values reach the child in plaintext, which a deploy does not do", got)
		}
		if strings.Contains(got, ".gitignore") {
			t.Errorf("notice = %q, want no gitignore warning when the file is already ignored", got)
		}
	})

	t.Run("it warns when the file is not ignored", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		reportDevValues(&out, t.TempDir(), dotfileValues(map[string]string{"API_TOKEN": "x"}), true)

		if got := out.String(); !strings.Contains(got, ".gitignore") {
			t.Errorf("notice = %q, want it to say the file is not ignored by git", got)
		}

		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env*\n!.env\n"), 0o644); err != nil {
			t.Fatalf("write .gitignore: %v", err)
		}
		var reincluded bytes.Buffer
		reportDevValues(&reincluded, dir, dotfileValues(map[string]string{"API_TOKEN": "x"}), true)
		if got := reincluded.String(); !strings.Contains(got, ".gitignore") {
			t.Errorf("notice = %q, want the warning when a later line re-includes the file", got)
		}
	})

	t.Run("it names where each value came from, and checks "+dotenv.LocalFileName+" against .gitignore on its own", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
			t.Fatalf("write .gitignore: %v", err)
		}
		var out bytes.Buffer
		reportDevValues(&out, dir, devValues{
			{from: "infisical:p-1/dev", values: map[string]string{"API_TOKEN": "sk-live-must-not-appear"}},
			{from: dotenv.LocalFileName, file: true, values: map[string]string{"LOG_LEVEL": "debug"}},
		}, true)
		got := out.String()

		for _, want := range []string{"API_TOKEN from infisical:p-1/dev", "LOG_LEVEL from " + dotenv.LocalFileName, dotenv.LocalFileName + " is not matched by this project's .gitignore", "editing " + dotenv.LocalFileName + " re-resolves"} {
			if !strings.Contains(got, want) {
				t.Errorf("notice = %q, want it to say %q", got, want)
			}
		}
		if strings.Contains(got, "sk-live") {
			t.Fatalf("notice = %q, want it to disclose no value", got)
		}

		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.local\n"), 0o644); err != nil {
			t.Fatalf("write .gitignore: %v", err)
		}
		var ignored bytes.Buffer
		reportDevValues(&ignored, dir, devValues{{from: dotenv.LocalFileName, file: true, values: map[string]string{"LOG_LEVEL": "debug"}}}, false)
		if strings.Contains(ignored.String(), ".gitignore") {
			t.Errorf("notice = %q, want no warning for a file a glob ignores", ignored.String())
		}
	})
}

func TestReportUnreadableLines(t *testing.T) {
	t.Parallel()

	t.Run("it names them by number only", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		reportUnreadableLines(&out, devValues{{from: dotenv.FileName, file: true, unreadable: []int{2, 5}}})
		got := out.String()

		for _, want := range []string{dotenv.FileName, "2, 5"} {
			if !strings.Contains(got, want) {
				t.Errorf("notice = %q, want it to mention %q", got, want)
			}
		}
	})

	t.Run("a single line is reported singularly", func(t *testing.T) {
		t.Parallel()

		var one bytes.Buffer
		reportUnreadableLines(&one, devValues{{from: dotenv.LocalFileName, file: true, unreadable: []int{4}}})
		if !strings.Contains(one.String(), dotenv.LocalFileName) {
			t.Errorf("notice = %q, want the file named", one.String())
		}
		if !strings.Contains(one.String(), "line 4 is") {
			t.Errorf("notice = %q, want a singular line reported singularly", one.String())
		}
	})
}

func TestCheckStatableBinding(t *testing.T) {
	t.Parallel()

	apps := []projectconfig.App{
		{Name: "web", Path: "apps/web", Folder: "/web"},
		{Name: "api", Path: "apps/api", Folder: "/api"},
	}

	t.Run("it refuses when the apps do not agree on one", func(t *testing.T) {
		t.Parallel()

		if err := checkStatableBinding(apps, "", projectconfig.DefaultFileName, nil); err != nil {
			t.Errorf("checkStatableBinding = %v, want nil with no scoped variable declared", err)
		}

		agreed := []projectconfig.App{{Name: "web", Folder: "/web"}, {Name: "admin", Folder: "/web"}}
		if err := checkStatableBinding(agreed, "/web", projectconfig.DefaultFileName, map[string][]string{"API_BASE": {"/web"}}); err != nil {
			t.Errorf("checkStatableBinding = %v, want nil when every app binds the folder dev states", err)
		}

		err := checkStatableBinding(apps, "", projectconfig.DefaultFileName, map[string][]string{"API_BASE": {"/web", "/api"}})
		if err == nil {
			t.Fatal("checkStatableBinding = nil, want a refusal: no child of this run could read API_BASE")
		}
		got := err.Error()
		for _, want := range []string{"API_BASE", "web binds /web", "api binds /api", "the project root", dotenv.FileName, "ocel.json"} {
			if !strings.Contains(got, want) {
				t.Errorf("refusal = %q, want it to mention %q", got, want)
			}
		}
		if strings.Contains(got, "one `ocel dev` per app") || strings.Contains(got, "per app") {
			t.Errorf("refusal = %q, want no per-app remedy: election keys on the project root, so a second run is a follower", got)
		}
	})

	t.Run("it starts when no app binds the key's scope", func(t *testing.T) {
		t.Parallel()

		if err := checkStatableBinding(apps, "", projectconfig.DefaultFileName, map[string][]string{"NOBODY": {"/nowhere"}}); err != nil {
			t.Errorf("checkStatableBinding = %v, want nil: no app binds /nowhere, so no read is lost", err)
		}

		scoped := map[string][]string{"NOBODY": {"/nowhere"}, "API_BASE": {"/web"}}
		err := checkStatableBinding(apps, "", projectconfig.DefaultFileName, scoped)
		if err == nil {
			t.Fatal("checkStatableBinding = nil, want a refusal for API_BASE, which web would read under its own binding")
		}
		if strings.Contains(err.Error(), "NOBODY") {
			t.Errorf("refusal = %q, want it silent about NOBODY: naming a key no app binds sends the developer after nothing", err.Error())
		}
	})

	t.Run("it names only the apps binding the key's scope", func(t *testing.T) {
		t.Parallel()

		err := checkStatableBinding(apps, "", projectconfig.DefaultFileName, map[string][]string{"API_BASE": {"/web"}})
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
	})

	t.Run("it lists every losing key and app in a fixed order", func(t *testing.T) {
		t.Parallel()

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
			"fix: bind every app to the same folder in ocel.json, or drop `folders:` from those declarations"

		for range 50 {
			err := checkStatableBinding(apps, "", projectconfig.DefaultFileName, scoped)
			if err == nil {
				t.Fatal("checkStatableBinding = nil, want a refusal: both apps lose a read")
			}
			if got := err.Error(); got != want {
				t.Fatalf("refusal =\n%q\nwant\n%q", got, want)
			}
		}
	})
}

func TestRunDevEnvironment(t *testing.T) {
	t.Run("the dotfile and the app folder reach the child", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app", apps: [{ name: "web", path: "apps/web", folder: "/web" }] };
`)
		clitest.WriteFile(t, filepath.Join(root, ".env"), "NEXT_PUBLIC_SITE_URL=https://example.com\nAWS_PROFILE=dev\napi_base=lower\nAPI_BASE=http://localhost:3000\nnot an assignment\n")
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"API_BASE","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}`))

		envDumpPath := filepath.Join(root, "env.out")
		appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), deps, false, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitsig.ExitError
		if !errors.As(err, &exitErr) || exitErr.Code != 7 {
			t.Fatalf("runDev err = %v, want exit 7; stderr=%s", err, stderr.String())
		}

		dumped, readErr := os.ReadFile(envDumpPath)
		if readErr != nil {
			t.Fatalf("read env dump: %v", readErr)
		}
		env := toMap(strings.Split(strings.TrimRight(string(dumped), "\n"), "\n"))

		t.Run("the child reads the dotfile's value and the folder the only app binds", func(t *testing.T) {
			if env["API_BASE"] != "http://localhost:3000" {
				t.Errorf("API_BASE = %q, want the dotfile's value", env["API_BASE"])
			}
			if env[constants.AppFolderEnvName] != "/web" {
				t.Errorf("%s = %q, want the folder the only app binds", constants.AppFolderEnvName, env[constants.AppFolderEnvName])
			}
		})

		t.Run("the notice accounts for every line of the file", func(t *testing.T) {
			if !strings.Contains(stdout.String(), "API_BASE") {
				t.Errorf("stdout = %q, want the divergence notice to name the key", stdout.String())
			}
			if !strings.Contains(stdout.String(), "line 5") {
				t.Errorf("stdout = %q, want the line that assigns nothing reported by number", stdout.String())
			}
			if strings.Contains(stdout.String(), "api_base") {
				t.Errorf("stdout = %q, want a line Ocel could never be asked for passed over in silence", stdout.String())
			}
			if !strings.Contains(stdout.String(), "NEXT_PUBLIC_SITE_URL") {
				t.Errorf("stdout = %q, want a declarable key accounted for", stdout.String())
			}
			if !strings.Contains(stdout.String(), devValues{{from: dotenv.FileName, file: true}, {from: dotenv.LocalFileName, file: true}}.advice(true)) {
				t.Errorf("stdout = %q, want the advice for a run that re-resolves on save", stdout.String())
			}
		})
	})

	t.Run("it refuses when the dotfile does not hold a required value", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"DATABASE_URL","class":"VARIABLE_CLASS_PLAIN","required":true}`))

		startedPath := filepath.Join(root, "started")
		appCmd := []string{"sh", "-c", "touch " + startedPath}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), deps, false, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		if err == nil {
			t.Fatal("runDev = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), dotenv.FileName) {
			t.Errorf("err = %q, want it to name DATABASE_URL and %s", err.Error(), dotenv.FileName)
		}
		if _, statErr := os.Stat(startedPath); statErr == nil {
			t.Error("the app was started despite the refusal")
		}
	})

	t.Run("it refuses a scoped variable no child of the run could read", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app", apps: [{ name: "web", path: "apps/web", folder: "/web" }, { name: "api", path: "apps/api", folder: "/api" }] };
`)
		clitest.WriteFile(t, filepath.Join(root, ".env"), "API_BASE=http://localhost:3000\n")
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"API_BASE","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}`))

		startedPath := filepath.Join(root, "started")
		appCmd := []string{"sh", "-c", "touch " + startedPath}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), deps, false, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		if err == nil {
			t.Fatal("runDev = nil, want a refusal rather than a green gate and a throw at the first read")
		}
		if !strings.Contains(err.Error(), "API_BASE") {
			t.Errorf("err = %q, want it to name the scoped key", err.Error())
		}
		if _, statErr := os.Stat(startedPath); statErr == nil {
			t.Error("the app was started despite the refusal")
		}
	})

	t.Run("it starts when a scoped variable is bound by no app", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app", apps: [{ name: "web", path: "apps/web", folder: "/web" }, { name: "api", path: "apps/api", folder: "/api" }] };
`)
		clitest.WriteFile(t, filepath.Join(root, ".env"), "NOBODY=x\n")
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"NOBODY","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/nowhere"]}`))

		startedPath := filepath.Join(root, "started")
		appCmd := []string{"sh", "-c", "touch " + startedPath + "; exit 7"}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), deps, false, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitsig.ExitError
		if !errors.As(err, &exitErr) || exitErr.Code != 7 {
			t.Fatalf("runDev err = %v, want the app to have run and exited 7; stderr=%s", err, stderr.String())
		}
		if _, statErr := os.Stat(startedPath); statErr != nil {
			t.Errorf("the app was not started: %v", statErr)
		}
	})

	t.Run("a dev source's value satisfies the gate without a dotfile", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		counted := filepath.Join(root, "reads")
		writeDevSource(t, root, `{ exec: { command: ["sh", "-c", "echo read >> `+counted+`; printf 'STRIPE_API_KEY=sk_from_source'"], format: "dotenv" } }`)
		clitest.WriteFile(t, filepath.Join(root, ".env"), "STRIPE_API_KEY=sk_from_dotenv\n")
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}`))

		env, stdout := dumpDevEnv(t, devDeps(), root)
		if env["STRIPE_API_KEY"] != "sk_from_source" {
			t.Errorf("STRIPE_API_KEY = %q, want the dev source's value, and .env left unread under another source", env["STRIPE_API_KEY"])
		}
		if !strings.Contains(stdout, "STRIPE_API_KEY") || !strings.Contains(stdout, "exec") {
			t.Errorf("stdout = %q, want it to say STRIPE_API_KEY came from exec", stdout)
		}
		if strings.Contains(stdout, "sk_from_source") {
			t.Errorf("stdout = %q, want no value printed", stdout)
		}
		if reads := strings.Count(readTestFile(t, counted), "read"); reads != 1 {
			t.Errorf("the source was read %d times, want once for the run", reads)
		}
	})

	t.Run("a folder's value outranks the root's for the folder the app binds", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: "apps/web", folder: "/web" }],
  envSource: { dev: { exec: { command: ["sh", "-c", "if [ \"$1\" = /web ]; then printf 'API_BASE=from-web'; else printf 'API_BASE=from-root\\nROOT_ONLY=r'; fi", "exec", "{folder}"], format: "dotenv" } } },
};
`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"API_BASE","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}`))

		env, _ := dumpDevEnv(t, devDeps(), root)
		if env["API_BASE"] != "from-web" || env["ROOT_ONLY"] != "r" {
			t.Errorf("API_BASE = %q, ROOT_ONLY = %q, want /web's value over the root's, and the root's where /web holds none", env["API_BASE"], env["ROOT_ONLY"])
		}
	})

	t.Run(dotenv.LocalFileName+" outranks the dev source", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		writeDevSource(t, root, `{ exec: { command: ["sh", "-c", "printf 'STRIPE_API_KEY=sk_from_source\\nLOG_LEVEL=info'"], format: "dotenv" } }`)
		clitest.WriteFile(t, filepath.Join(root, dotenv.LocalFileName), "STRIPE_API_KEY=sk_mine\n")
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}`))

		env, stdout := dumpDevEnv(t, devDeps(), root)
		if env["STRIPE_API_KEY"] != "sk_mine" || env["LOG_LEVEL"] != "info" {
			t.Errorf("STRIPE_API_KEY = %q, LOG_LEVEL = %q, want %s over the source and the source beneath it", env["STRIPE_API_KEY"], env["LOG_LEVEL"], dotenv.LocalFileName)
		}
		if !strings.Contains(stdout, dotenv.LocalFileName) {
			t.Errorf("stdout = %q, want it to say what came from %s", stdout, dotenv.LocalFileName)
		}
	})

	t.Run(dotenv.LocalFileName+" outranks .env under the default source", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		clitest.WriteFile(t, filepath.Join(root, dotenv.FileName), "STRIPE_API_KEY=sk_shared\nLOG_LEVEL=info\n")
		clitest.WriteFile(t, filepath.Join(root, dotenv.LocalFileName), "STRIPE_API_KEY=sk_mine\n")
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}`))

		env, _ := dumpDevEnv(t, devDeps(), root)
		if env["STRIPE_API_KEY"] != "sk_mine" || env["LOG_LEVEL"] != "info" {
			t.Errorf("STRIPE_API_KEY = %q, LOG_LEVEL = %q, want %s over %s", env["STRIPE_API_KEY"], env["LOG_LEVEL"], dotenv.LocalFileName, dotenv.FileName)
		}
	})

	t.Run("a dev source that cannot be read stops the run and says why", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		writeDevSource(t, root, `{ exec: { command: ["sh", "-c", "echo 'vault is sealed' >&2; exit 3"], format: "json" } }`)
		startedPath := filepath.Join(root, "started")

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), devDeps(), false, root, []string{"sh", "-c", "touch " + startedPath}, &stdout, &stderr, strings.NewReader(""))
		if err == nil || !strings.Contains(err.Error(), "vault is sealed") {
			t.Fatalf("runDev err = %v, want the source's own complaint", err)
		}
		if _, statErr := os.Stat(startedPath); statErr == nil {
			t.Error("the app was started without its dev source")
		}
	})

	t.Run("an Infisical dev source with no way in says how to sign in", func(t *testing.T) {
		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		clitest.WriteFile(t, filepath.Join(root, projectconfig.DefaultFileName), `{"slug":"test-app","envSource":{"dev":{"infisical":{"project":"p-1","environment":"dev"}}}}`)
		t.Setenv("PATH", t.TempDir())
		t.Setenv("INFISICAL_TOKEN", "")

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), devDeps(), false, root, []string{"true"}, &stdout, &stderr, strings.NewReader(""))
		if err == nil || !strings.Contains(err.Error(), "INFISICAL_TOKEN") || !strings.Contains(err.Error(), "infisical login") {
			t.Fatalf("runDev err = %v, want both ways in named", err)
		}
	})

	t.Run("an Infisical dev source reads with the token in the shell", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer developer-token" || r.URL.Path != "/api/v4/secrets" || r.URL.Query().Get("environment") != "dev" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"secrets": []map[string]any{
				{"id": "s1", "secretKey": "STRIPE_API_KEY", "secretValue": "sk_from_infisical", "version": 1},
			}})
		}))
		t.Cleanup(server.Close)

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		t.Setenv("INFISICAL_TOKEN", "developer-token")
		writeDevSource(t, root, `{ infisical: { project: "p-1", environment: "dev", host: "`+server.URL+`" } }`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}`))

		env, stdout := dumpDevEnv(t, devDeps(), root)
		if env["STRIPE_API_KEY"] != "sk_from_infisical" {
			t.Errorf("STRIPE_API_KEY = %q, want Infisical's value", env["STRIPE_API_KEY"])
		}
		if !strings.Contains(stdout, "infisical:p-1/dev") {
			t.Errorf("stdout = %q, want it to name the Infisical source", stdout)
		}
	})

	t.Run("a live-class key is not refused for having no local value", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"DB_PASSWORD","class":"VARIABLE_CLASS_SECRET","required":true}`))

		startedPath := filepath.Join(root, "started")
		appCmd := []string{"sh", "-c", "touch " + startedPath + "; exit 7"}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), deps, false, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitsig.ExitError
		if !errors.As(err, &exitErr) || exitErr.Code != 7 {
			t.Fatalf("runDev err = %v, want exit 7 (no refusal); stderr=%s", err, stderr.String())
		}
		if _, statErr := os.Stat(startedPath); statErr != nil {
			t.Fatalf("the app was not started: %v", statErr)
		}
		if !strings.Contains(stdout.String(), "DB_PASSWORD") {
			t.Errorf("stdout = %q, want the live-value notice to name DB_PASSWORD", stdout.String())
		}
	})

	t.Run("it generates the client accessor and exports the value under its declared name", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(root, "tsconfig.json"), "{\n  \"compilerOptions\": {}\n}\n")
		clitest.WriteFile(t, filepath.Join(root, ".env"), "PUBLIC_SITE_URL=https://local.example.com\nSTRIPE_API_KEY=sk_local\n")
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(
			`{"key":"PUBLIC_SITE_URL","class":"VARIABLE_CLASS_PLAIN","required":true,"clientAccessible":true}`,
			`{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}`,
		))

		envDumpPath := filepath.Join(root, "env.out")
		appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), deps, false, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitsig.ExitError
		if !errors.As(err, &exitErr) || exitErr.Code != 7 {
			t.Fatalf("runDev err = %v, want exit 7 (no refusal); stderr=%s", err, stderr.String())
		}

		accessor, readErr := os.ReadFile(filepath.Join(root, constants.ProjectStateDirName, "env-client.ts"))
		if readErr != nil {
			t.Fatalf("dev generated no client accessor: %v", readErr)
		}
		if !strings.Contains(string(accessor), `PUBLIC_SITE_URL: inlined(schema, "PUBLIC_SITE_URL", process.env.PUBLIC_SITE_URL)`) {
			t.Errorf("accessor = %s, want it to read the key under its declared name", accessor)
		}
		if strings.Contains(string(accessor), "STRIPE_API_KEY") {
			t.Errorf("accessor names a server-only value:\n%s", accessor)
		}
		if tsconfig := readTestFile(t, filepath.Join(root, "tsconfig.json")); !strings.Contains(tsconfig, `"ocel/env/client": ["./`+constants.ProjectStateDirName+`/env-client.ts"]`) {
			t.Errorf("tsconfig does not point the import at the accessor:\n%s", tsconfig)
		}

		dumped, readErr := os.ReadFile(envDumpPath)
		if readErr != nil {
			t.Fatalf("read env dump: %v", readErr)
		}
		env := toMap(strings.Split(strings.TrimRight(string(dumped), "\n"), "\n"))
		if got, want := env["PUBLIC_SITE_URL"], "https://local.example.com"; got != want {
			t.Errorf("PUBLIC_SITE_URL = %q, want %q — without it the accessor refuses to load", got, want)
		}
		if _, ok := env["NEXT_PUBLIC_PUBLIC_SITE_URL"]; ok {
			t.Error("a value was exported under a prefixed name; a key is delivered as it was declared")
		}
	})
}

func TestRunRunEnvironment(t *testing.T) {
	t.Run("it starts when a scoped variable is bound by no app", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app", apps: [{ name: "web", path: "apps/web", folder: "/web" }, { name: "api", path: "apps/api", folder: "/api" }] };
`)
		clitest.WriteFile(t, filepath.Join(root, ".env"), "NOBODY=x\n")
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"NOBODY","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/nowhere"]}`))

		startedPath := filepath.Join(root, "started")
		appCmd := []string{"sh", "-c", "touch " + startedPath + "; exit 7"}

		var stdout, stderr bytes.Buffer
		err := runRun(context.Background(), deps, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitsig.ExitError
		if !errors.As(err, &exitErr) || exitErr.Code != 7 {
			t.Fatalf("runRun err = %v, want the app to have run and exited 7; stderr=%s", err, stderr.String())
		}
		if _, statErr := os.Stat(startedPath); statErr != nil {
			t.Errorf("the app was not started: %v", statErr)
		}
	})

	t.Run("it resolves the dotfile and gates like dev", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app", apps: [{ name: "web", path: "apps/web", folder: "/web" }] };
`)
		clitest.WriteFile(t, filepath.Join(root, ".env"), "API_BASE=http://localhost:3000\n")
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(
			`{"key":"API_BASE","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}`))

		envDumpPath := filepath.Join(root, "env.out")
		appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

		var stdout, stderr bytes.Buffer
		err := runRun(context.Background(), deps, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitsig.ExitError
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
		if env[constants.AppFolderEnvName] != "/web" {
			t.Errorf("%s = %q, want the folder the only app binds", constants.AppFolderEnvName, env[constants.AppFolderEnvName])
		}
		if !strings.Contains(stdout.String(), devValues{{from: dotenv.FileName, file: true}, {from: dotenv.LocalFileName, file: true}}.advice(false)) {
			t.Errorf("stdout = %q, want the advice for a run that reads the file once", stdout.String())
		}
	})

	t.Run("it refuses when the dotfile does not hold a required value", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"DATABASE_URL","class":"VARIABLE_CLASS_PLAIN","required":true}`))

		startedPath := filepath.Join(root, "started")
		appCmd := []string{"sh", "-c", "touch " + startedPath}

		var stdout, stderr bytes.Buffer
		err := runRun(context.Background(), deps, root, appCmd, &stdout, &stderr, strings.NewReader(""))

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
	})

	t.Run("it resolves the dev source like dev", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		writeDevSource(t, root, `{ exec: { command: ["sh", "-c", "printf '{\"STRIPE_API_KEY\":\"sk_from_source\"}'"], format: "json" } }`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}`))

		envDumpPath := filepath.Join(root, "env.out")
		var stdout, stderr syncBuffer
		err := runRun(context.Background(), devDeps(), root, []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitsig.ExitError
		if !errors.As(err, &exitErr) || exitErr.Code != 7 {
			t.Fatalf("runRun err = %v, want exit 7 (no refusal); stderr=%s", err, stderr.String())
		}
		env := toMap(strings.Split(strings.TrimRight(readTestFile(t, envDumpPath), "\n"), "\n"))
		if env["STRIPE_API_KEY"] != "sk_from_source" {
			t.Errorf("STRIPE_API_KEY = %q, want the dev source's value", env["STRIPE_API_KEY"])
		}
	})
}

func writeDevSource(t *testing.T, root, descriptor string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), "export default { slug: \"test-app\", envSource: { dev: "+descriptor+" } };\n")
}

func dumpDevEnv(t *testing.T, deps cmddeps.Deps, root string) (map[string]string, string) {
	t.Helper()
	envDumpPath := filepath.Join(root, "env.out")
	var stdout, stderr syncBuffer
	err := runDev(context.Background(), deps, false, root, []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}, &stdout, &stderr, strings.NewReader(""))
	var exitErr *exitsig.ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("runDev err = %v, want exit 7 (no refusal); stderr=%s", err, stderr.String())
	}
	return toMap(strings.Split(strings.TrimRight(readTestFile(t, envDumpPath), "\n"), "\n")), stdout.String()
}

func TestDevGivesEveryAppItsURL(t *testing.T) {
	node := envgate.Scope{Apps: []envgate.App{{Name: "web", ClientBundle: true}}}

	t.Run("localhost on the default port where nothing names one", func(t *testing.T) {
		t.Setenv("PORT", "")

		got := resolvedEnv(nil, nil, nil, runtimeAccess{}, "", node)
		for _, key := range []string{constants.AppURLEnvName, providerkit.ClientURLEnvName} {
			if want := "http://localhost:3000"; got[key] != want {
				t.Errorf("%s = %q, want %q — dev never leaves it unset, so an app may read it without a fallback", key, got[key], want)
			}
		}
	})

	t.Run("the browser's copy only where an app's bundle reads it", func(t *testing.T) {
		t.Setenv("PORT", "")
		dotfile := map[string]string{providerkit.ClientURLEnvName: "https://mine.example"}

		got := resolvedEnv(nil, dotfile, nil, runtimeAccess{}, "", envgate.Scope{Apps: []envgate.App{{Name: "api"}}})
		if want := "http://localhost:3000"; got[constants.AppURLEnvName] != want {
			t.Errorf("%s = %q, want %q for every app", constants.AppURLEnvName, got[constants.AppURLEnvName], want)
		}
		if want := "https://mine.example"; got[providerkit.ClientURLEnvName] != want {
			t.Errorf("%s = %q, want the go app's own %q: nothing in a go app reads ocel's copy, so writing one overwrites its value", providerkit.ClientURLEnvName, got[providerkit.ClientURLEnvName], want)
		}
	})

	t.Run("the port the project names", func(t *testing.T) {
		t.Setenv("PORT", "")

		got := resolvedEnv(nil, map[string]string{"PORT": "4321"}, nil, runtimeAccess{}, "", node)
		if want := "http://localhost:4321"; got[constants.AppURLEnvName] != want {
			t.Errorf("%s = %q, want %q", constants.AppURLEnvName, got[constants.AppURLEnvName], want)
		}
	})

	t.Run("the port the shell exports", func(t *testing.T) {
		t.Setenv("PORT", "8080")

		got := resolvedEnv(nil, nil, nil, runtimeAccess{}, "", node)
		if want := "http://localhost:8080"; got[constants.AppURLEnvName] != want {
			t.Errorf("%s = %q, want %q — the app is spawned with the shell's environment under it", constants.AppURLEnvName, got[constants.AppURLEnvName], want)
		}
	})
}

func TestRunWritesTheBrowsersURLForTheAppItRunsIn(t *testing.T) {
	t.Setenv("PORT", "")
	root := t.TempDir()
	cfg := &projectconfig.Config{Dir: root, Apps: []projectconfig.App{
		{Name: "web", Path: filepath.Join("apps", "web"), Folder: "/web", Runtime: projectconfig.Runtime{Name: providerkit.RuntimeNext}},
		{Name: "api", Path: filepath.Join("apps", "api"), Folder: "/api", Runtime: projectconfig.Runtime{Name: providerkit.RuntimeGo}},
		{Name: "webhooks", Path: filepath.Join("apps", "web-hooks"), Runtime: projectconfig.Runtime{Name: providerkit.RuntimePython}},
		{Name: "store", Path: filepath.Join("apps", "store"), Folder: "/store", Compute: string(providerkit.ComputeContainer)},
		{Name: "worker", Path: filepath.Join("apps", "worker"), Folder: "/worker", Compute: string(providerkit.ComputeContainer)},
	}}
	writeApp(t, filepath.Join(root, "apps", "store", "package.json"), "{}")
	writeApp(t, filepath.Join(root, "apps", "worker", "go.mod"), "module example.com/worker")

	for _, tc := range []struct {
		name    string
		cwd     string
		written bool
	}{
		{name: "inside the go app", cwd: filepath.Join(root, "apps", "api", "cmd"), written: false},
		{name: "inside an app whose path only shares a prefix with the next app's", cwd: filepath.Join(root, "apps", "web-hooks"), written: false},
		{name: "inside the next app", cwd: filepath.Join(root, "apps", "web"), written: true},
		{name: "inside a container app whose directory holds a package.json", cwd: filepath.Join(root, "apps", "store"), written: true},
		{name: "inside a container app whose directory holds a go.mod", cwd: filepath.Join(root, "apps", "worker"), written: false},
		{name: "at the project root, where no one app is the target", cwd: root, written: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvedEnv(nil, nil, nil, runtimeAccess{}, "", targetScope(cfg, tc.cwd))
			if _, written := got[providerkit.ClientURLEnvName]; written != tc.written {
				t.Errorf("%s written = %v, want %v — a command run inside one app reads only that app's runtime, and elsewhere any app in the project", providerkit.ClientURLEnvName, written, tc.written)
			}
		})
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeApp(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
