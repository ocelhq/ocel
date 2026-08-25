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

	"github.com/ocelhq/ocel/cli/internal/cli/session"
	"github.com/ocelhq/ocel/cli/internal/credentials"
	"github.com/ocelhq/ocel/cli/internal/dotenv"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/lockfile"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/resolve"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func withCredentials(sess *session.Session, apiURL string) {
	sess.LoadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{APIURL: apiURL, AccessToken: "tok"}, nil
	}
}

func withProjectEnv(sess *session.Session, envVars map[string]string) *atomic.Int32 {
	var calls atomic.Int32
	sess.FetchAccount = func(_ context.Context, apiURL, token, projectID string) (resolve.Account, error) {
		calls.Add(1)
		return resolve.Account{ProjectID: projectID, EnvVars: envVars, APIURL: apiURL, Token: token}, nil
	}
	return &calls
}

func declareEnvScript(definitions ...string) string {
	return fmt.Sprintf(`
declare global {
  var __ocelRegister: Promise<unknown>[];
}
globalThis.__ocelRegister ??= [];
globalThis.__ocelRegister.push(
  fetch(new URL("/app.resources.v1.ResourceService/DeclareEnv", process.env.OCEL_DEV_SERVER), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ definitions: [%s] }),
  }),
);
export {};
`, strings.Join(definitions, ","))
}

func TestResolvedEnv(t *testing.T) {
	t.Parallel()

	t.Run("the dotfile outranks every other source but a resource", func(t *testing.T) {
		t.Parallel()

		base := []string{"PATH=/bin", "CONTESTED=shell", "SHELL_ONLY=s"}
		projectEnv := map[string]string{"CONTESTED": "project"}
		live := map[string]string{"CONTESTED": "live"}
		dotfile := map[string]string{"CONTESTED": "dotfile", "DOTFILE_ONLY": "d"}
		resources := []resolve.Resource{
			{Name: "main", Env: map[string]string{"OCEL_RESOURCE_POSTGRES_main": "conn"}},
		}

		got := toMap(mergeEnv(base, projectEnv, live, dotfile, resources, "", ""))

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

		projectEnv := map[string]string{"PROJECT_ONLY": "p", "OVERRIDDEN": "from-project"}
		live := map[string]string{"WEBHOOK_SECRET": "whsec_live", "OVERRIDDEN": "from-live"}
		resources := []resolve.Resource{
			{Name: "main", Env: map[string]string{"OCEL_RESOURCE_POSTGRES_main": "conn"}},
		}

		got := resolvedEnv(projectEnv, live, nil, resources, "", "")

		cases := map[string]string{
			"PROJECT_ONLY":                "p",
			"WEBHOOK_SECRET":              "whsec_live",
			"OVERRIDDEN":                  "from-live",
			"OCEL_RESOURCE_POSTGRES_main": "conn",
		}
		for k, want := range cases {
			if got[k] != want {
				t.Errorf("env[%q] = %q, want %q", k, got[k], want)
			}
		}
	})

	t.Run("the app folder is always stated", func(t *testing.T) {
		t.Parallel()

		bound := resolvedEnv(nil, nil, nil, nil, "", "/web")
		if bound["OCEL_APP_FOLDER"] != "/web" {
			t.Errorf("OCEL_APP_FOLDER = %q, want %q", bound["OCEL_APP_FOLDER"], "/web")
		}

		unbound := resolvedEnv(nil, nil, nil, nil, "", "")
		folder, ok := unbound["OCEL_APP_FOLDER"]
		if !ok {
			t.Fatalf("resolvedEnv = %v, want OCEL_APP_FOLDER written even for an unbound app", unbound)
		}
		if folder != "" {
			t.Errorf("OCEL_APP_FOLDER = %q, want the project root spelled as the empty string", folder)
		}

		stale := toMap(mergeEnv([]string{"OCEL_APP_FOLDER=/stale"}, nil, nil, nil, nil, "", ""))
		if stale["OCEL_APP_FOLDER"] != "" {
			t.Errorf("OCEL_APP_FOLDER = %q, want the shell's stale binding overwritten", stale["OCEL_APP_FOLDER"])
		}

		contested := resolvedEnv(
			map[string]string{"OCEL_APP_FOLDER": "/from-project-env"},
			map[string]string{"OCEL_APP_FOLDER": "/from-live"},
			map[string]string{"OCEL_APP_FOLDER": "/from-dotfile"},
			[]resolve.Resource{{Name: "main", Env: map[string]string{"OCEL_APP_FOLDER": "/from-resource"}}},
			"",
			"/web",
		)
		if contested["OCEL_APP_FOLDER"] != "/web" {
			t.Errorf("OCEL_APP_FOLDER = %q, want the binding dev states to outrank every source it merges", contested["OCEL_APP_FOLDER"])
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
	})

	t.Run("it says so when the key is only in the shell", func(t *testing.T) {
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

		inFile := devRefusal(refusal, dotfileKeySet(map[string]string{"DATABASE_URL": "postgres://from-the-file"})).Error()
		if strings.Contains(inFile, "set in this shell") {
			t.Errorf("refusal = %q, want no shell hint for a key the file does hold", inFile)
		}
	})

	t.Run("it is never given a value it could print", func(t *testing.T) {
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
	})
}

func TestReportDotfile(t *testing.T) {
	t.Parallel()

	t.Run("it states what the file costs and prints no value", func(t *testing.T) {
		t.Parallel()

		var quiet bytes.Buffer
		reportDotfile(&quiet, t.TempDir(), nil, dotfileWatchedAdvice)
		if quiet.Len() != 0 {
			t.Errorf("reportDotfile wrote %q for a run with no dotfile values, want nothing", quiet.String())
		}

		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
			t.Fatalf("write .gitignore: %v", err)
		}

		var out bytes.Buffer
		reportDotfile(&out, dir, map[string]string{"API_TOKEN": "sk-live-must-not-appear", "DATABASE_URL": "postgres://secret"}, dotfileWatchedAdvice)
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
		reportDotfile(&out, t.TempDir(), map[string]string{"API_TOKEN": "x"}, dotfileWatchedAdvice)

		if got := out.String(); !strings.Contains(got, ".gitignore") {
			t.Errorf("notice = %q, want it to say the file is not ignored by git", got)
		}

		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env*\n!.env\n"), 0o644); err != nil {
			t.Fatalf("write .gitignore: %v", err)
		}
		var reincluded bytes.Buffer
		reportDotfile(&reincluded, dir, map[string]string{"API_TOKEN": "x"}, dotfileWatchedAdvice)
		if got := reincluded.String(); !strings.Contains(got, ".gitignore") {
			t.Errorf("notice = %q, want the warning when a later line re-includes the file", got)
		}
	})
}

func TestReportUnreadableLines(t *testing.T) {
	t.Parallel()

	t.Run("it names them by number only", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		reportUnreadableLines(&out, []int{2, 5})
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
		reportUnreadableLines(&one, []int{4})
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

		if err := checkStatableBinding(apps, "", projectconfig.ConfigFileName, nil); err != nil {
			t.Errorf("checkStatableBinding = %v, want nil with no scoped variable declared", err)
		}

		agreed := []projectconfig.App{{Name: "web", Folder: "/web"}, {Name: "admin", Folder: "/web"}}
		if err := checkStatableBinding(agreed, "/web", projectconfig.ConfigFileName, map[string][]string{"API_BASE": {"/web"}}); err != nil {
			t.Errorf("checkStatableBinding = %v, want nil when every app binds the folder dev states", err)
		}

		err := checkStatableBinding(apps, "", projectconfig.ConfigFileName, map[string][]string{"API_BASE": {"/web", "/api"}})
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
	})

	t.Run("it starts when no app binds the key's scope", func(t *testing.T) {
		t.Parallel()

		if err := checkStatableBinding(apps, "", projectconfig.ConfigFileName, map[string][]string{"NOBODY": {"/nowhere"}}); err != nil {
			t.Errorf("checkStatableBinding = %v, want nil: no app binds /nowhere, so no read is lost", err)
		}

		scoped := map[string][]string{"NOBODY": {"/nowhere"}, "API_BASE": {"/web"}}
		err := checkStatableBinding(apps, "", projectconfig.ConfigFileName, scoped)
		if err == nil {
			t.Fatal("checkStatableBinding = nil, want a refusal for API_BASE, which web would read under its own binding")
		}
		if strings.Contains(err.Error(), "NOBODY") {
			t.Errorf("refusal = %q, want it silent about NOBODY: naming a key no app binds sends the developer after nothing", err.Error())
		}
	})

	t.Run("it names only the apps binding the key's scope", func(t *testing.T) {
		t.Parallel()

		err := checkStatableBinding(apps, "", projectconfig.ConfigFileName, map[string][]string{"API_BASE": {"/web"}})
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
			"fix: bind every app to the same folder in ocel.config.ts, or drop `folders:` from those declarations"

		for range 50 {
			err := checkStatableBinding(apps, "", projectconfig.ConfigFileName, scoped)
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

		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		sess := newSession()
		withCredentials(&sess, resolveServer.URL)
		writeLink(t, root, resolveServer.URL, testProjectID(t))
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app", apps: [{ name: "web", path: "apps/web", folder: "/web" }] };
`)
		clitest.WriteFile(t, filepath.Join(root, ".env"), "NEXT_PUBLIC_SITE_URL=https://example.com\nAWS_PROFILE=dev\napi_base=lower\nAPI_BASE=http://localhost:3000\nnot an assignment\n")
		clitest.WriteFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"API_BASE","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}`))

		envDumpPath := filepath.Join(root, "env.out")
		appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), sess, nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

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
			if env["OCEL_APP_FOLDER"] != "/web" {
				t.Errorf("OCEL_APP_FOLDER = %q, want the folder the only app binds", env["OCEL_APP_FOLDER"])
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
			if !strings.Contains(stdout.String(), dotfileWatchedAdvice) {
				t.Errorf("stdout = %q, want the advice for a run that re-resolves on save", stdout.String())
			}
		})
	})

	t.Run("it refuses when the dotfile does not hold a required value", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		sess := newSession()
		withCredentials(&sess, resolveServer.URL)
		writeLink(t, root, resolveServer.URL, testProjectID(t))
		clitest.WriteFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"DATABASE_URL","class":"VARIABLE_CLASS_PLAIN","required":true}`))

		startedPath := filepath.Join(root, "started")
		appCmd := []string{"sh", "-c", "touch " + startedPath}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), sess, nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

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

		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		sess := newSession()
		withCredentials(&sess, resolveServer.URL)
		writeLink(t, root, resolveServer.URL, testProjectID(t))
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app", apps: [{ name: "web", path: "apps/web", folder: "/web" }, { name: "api", path: "apps/api", folder: "/api" }] };
`)
		clitest.WriteFile(t, filepath.Join(root, ".env"), "API_BASE=http://localhost:3000\n")
		clitest.WriteFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"API_BASE","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}`))

		startedPath := filepath.Join(root, "started")
		appCmd := []string{"sh", "-c", "touch " + startedPath}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), sess, nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

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

		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		sess := newSession()
		withCredentials(&sess, resolveServer.URL)
		writeLink(t, root, resolveServer.URL, testProjectID(t))
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app", apps: [{ name: "web", path: "apps/web", folder: "/web" }, { name: "api", path: "apps/api", folder: "/api" }] };
`)
		clitest.WriteFile(t, filepath.Join(root, ".env"), "NOBODY=x\n")
		clitest.WriteFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"NOBODY","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/nowhere"]}`))

		startedPath := filepath.Join(root, "started")
		appCmd := []string{"sh", "-c", "touch " + startedPath + "; exit 7"}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), sess, nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitsig.ExitError
		if !errors.As(err, &exitErr) || exitErr.Code != 7 {
			t.Fatalf("runDev err = %v, want the app to have run and exited 7; stderr=%s", err, stderr.String())
		}
		if _, statErr := os.Stat(startedPath); statErr != nil {
			t.Errorf("the app was not started: %v", statErr)
		}
	})

	t.Run("a control plane value satisfies the gate without a dotfile", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		sess := newSession()
		withCredentials(&sess, resolveServer.URL)
		calls := withProjectEnv(&sess, map[string]string{"STRIPE_API_KEY": "sk_from_store"})
		writeLink(t, root, resolveServer.URL, testProjectID(t))
		clitest.WriteFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}`))

		envDumpPath := filepath.Join(root, "env.out")
		appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), sess, nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitsig.ExitError
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
	})

	t.Run("the dotfile still outranks the control plane at the gate", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		sess := newSession()
		withCredentials(&sess, resolveServer.URL)
		withProjectEnv(&sess, map[string]string{"STRIPE_API_KEY": "sk_from_store"})
		writeLink(t, root, resolveServer.URL, testProjectID(t))
		clitest.WriteFile(t, filepath.Join(root, ".env"), "STRIPE_API_KEY=sk_from_file\n")
		clitest.WriteFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}`))

		envDumpPath := filepath.Join(root, "env.out")
		appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), sess, nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitsig.ExitError
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
	})

	t.Run("it falls back to the dotfile when the control plane is unreachable", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		sess := newSession()
		withCredentials(&sess, resolveServer.URL)
		sess.FetchAccount = func(context.Context, string, string, string) (resolve.Account, error) {
			return resolve.Account{}, errors.New("dial tcp: connection refused")
		}

		writeLink(t, root, resolveServer.URL, testProjectID(t))
		clitest.WriteFile(t, filepath.Join(root, ".env"), "API_BASE=http://localhost:3000\n")
		clitest.WriteFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"API_BASE","class":"VARIABLE_CLASS_PLAIN","required":true}`))

		envDumpPath := filepath.Join(root, "env.out")
		appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), sess, nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitsig.ExitError
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
	})

	t.Run("a live-class key is not refused for having no local value", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		sess := newSession()
		withCredentials(&sess, resolveServer.URL)
		writeLink(t, root, resolveServer.URL, testProjectID(t))
		clitest.WriteFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"DB_PASSWORD","class":"VARIABLE_CLASS_SECRET","required":true}`))

		startedPath := filepath.Join(root, "started")
		appCmd := []string{"sh", "-c", "touch " + startedPath + "; exit 7"}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), sess, nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

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

		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		sess := newSession()
		withCredentials(&sess, resolveServer.URL)
		writeLink(t, root, resolveServer.URL, testProjectID(t))
		clitest.WriteFile(t, filepath.Join(root, "tsconfig.json"), "{\n  \"compilerOptions\": {}\n}\n")
		clitest.WriteFile(t, filepath.Join(root, ".env"), "PUBLIC_SITE_URL=https://local.example.com\nSTRIPE_API_KEY=sk_local\n")
		clitest.WriteFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(
			`{"key":"PUBLIC_SITE_URL","class":"VARIABLE_CLASS_PLAIN","required":true,"clientAccessible":true}`,
			`{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}`,
		))

		envDumpPath := filepath.Join(root, "env.out")
		appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), sess, nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitsig.ExitError
		if !errors.As(err, &exitErr) || exitErr.Code != 7 {
			t.Fatalf("runDev err = %v, want exit 7 (no refusal); stderr=%s", err, stderr.String())
		}

		accessor, readErr := os.ReadFile(filepath.Join(root, ".ocel", "env-client.ts"))
		if readErr != nil {
			t.Fatalf("dev generated no client accessor: %v", readErr)
		}
		if !strings.Contains(string(accessor), `PUBLIC_SITE_URL: inlined("PUBLIC_SITE_URL", process.env.PUBLIC_SITE_URL)`) {
			t.Errorf("accessor = %s, want it to read the key under its declared name", accessor)
		}
		if strings.Contains(string(accessor), "STRIPE_API_KEY") {
			t.Errorf("accessor names a server-only value:\n%s", accessor)
		}
		if tsconfig := readTestFile(t, filepath.Join(root, "tsconfig.json")); !strings.Contains(tsconfig, `"ocel/env/client": ["./.ocel/env-client.ts"]`) {
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

		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		sess := newSession()
		withCredentials(&sess, resolveServer.URL)
		writeLink(t, root, resolveServer.URL, testProjectID(t))
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app", apps: [{ name: "web", path: "apps/web", folder: "/web" }, { name: "api", path: "apps/api", folder: "/api" }] };
`)
		clitest.WriteFile(t, filepath.Join(root, ".env"), "NOBODY=x\n")
		clitest.WriteFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"NOBODY","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/nowhere"]}`))

		startedPath := filepath.Join(root, "started")
		appCmd := []string{"sh", "-c", "touch " + startedPath + "; exit 7"}

		var stdout, stderr bytes.Buffer
		err := runRun(context.Background(), sess, nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

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

		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		sess := newSession()
		withCredentials(&sess, resolveServer.URL)
		writeLink(t, root, resolveServer.URL, testProjectID(t))
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app", apps: [{ name: "web", path: "apps/web", folder: "/web" }] };
`)
		clitest.WriteFile(t, filepath.Join(root, ".env"), "API_BASE=http://localhost:3000\n")
		clitest.WriteFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(
			`{"key":"API_BASE","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}`))

		envDumpPath := filepath.Join(root, "env.out")
		appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

		var stdout, stderr bytes.Buffer
		err := runRun(context.Background(), sess, nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

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
		if env["OCEL_APP_FOLDER"] != "/web" {
			t.Errorf("OCEL_APP_FOLDER = %q, want the folder the only app binds", env["OCEL_APP_FOLDER"])
		}
		if !strings.Contains(stdout.String(), dotfileReadOnceAdvice) {
			t.Errorf("stdout = %q, want the advice for a run that reads the file once", stdout.String())
		}
	})

	t.Run("it refuses when the dotfile does not hold a required value", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		sess := newSession()
		withCredentials(&sess, resolveServer.URL)
		writeLink(t, root, resolveServer.URL, testProjectID(t))
		clitest.WriteFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"DATABASE_URL","class":"VARIABLE_CLASS_PLAIN","required":true}`))

		startedPath := filepath.Join(root, "started")
		appCmd := []string{"sh", "-c", "touch " + startedPath}

		var stdout, stderr bytes.Buffer
		err := runRun(context.Background(), sess, nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

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

	t.Run("a control plane value satisfies the gate without a dotfile", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		sess := newSession()
		withCredentials(&sess, resolveServer.URL)
		withProjectEnv(&sess, map[string]string{"STRIPE_API_KEY": "sk_from_store"})
		writeLink(t, root, resolveServer.URL, testProjectID(t))
		clitest.WriteFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}`))

		envDumpPath := filepath.Join(root, "env.out")
		appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

		var stdout, stderr syncBuffer
		err := runRun(context.Background(), sess, nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitsig.ExitError
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
	})
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
