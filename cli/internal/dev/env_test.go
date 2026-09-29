package dev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/dev/leader"
	"github.com/ocelhq/ocel/cli/internal/devresources/binding"
	"github.com/ocelhq/ocel/cli/internal/dotfile"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/statedir"

	"github.com/ocelhq/ocel/cli/internal/clitest"
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
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + process.env.`+processenv.DevServerTokenEnvVar+` },
    body: JSON.stringify({ definitions: [%s] }),
  }),
);
export {};
`, processenv.DevServerEnvVar, strings.Join(definitions, ","))
}

func TestResolvedEnv(t *testing.T) {
	t.Parallel()

	t.Run("the dev values outrank every other source but a resource", func(t *testing.T) {
		t.Parallel()

		base := []string{"PATH=/bin", "CONTESTED=shell", "SHELL_ONLY=s"}
		live := map[string]string{"CONTESTED": "live"}
		fileValues := map[string]string{"CONTESTED": "dotfile", "DOTFILE_ONLY": "d"}
		resources := []binding.Resolved{
			{Name: "main", Env: map[string]string{"OCEL_RESOURCE_POSTGRES_main": "conn"}},
		}

		got := toMap(applyEnv(base, resolvedEnv(live, fileValues, resources, runtimeAccess{}, "", variables.Scope{})))

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
		resources := []binding.Resolved{
			{Name: "main", Env: map[string]string{"OCEL_RESOURCE_POSTGRES_main": "conn"}},
		}

		got := resolvedEnv(live, values, resources, runtimeAccess{}, "", variables.Scope{})

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

		reached := resolvedEnv(nil, nil, nil, runtimeAccess{url: "http://127.0.0.1:4242", token: "app-token"}, "", variables.Scope{})
		if reached[processenv.RuntimeAddressEnvVar] != "http://127.0.0.1:4242" || reached[localrpc.SessionTokenEnvVar] != "app-token" {
			t.Errorf("env = %v, want %s and %s stated together", reached, processenv.RuntimeAddressEnvVar, localrpc.SessionTokenEnvVar)
		}

		unreached := resolvedEnv(nil, nil, nil, runtimeAccess{}, "", variables.Scope{})
		for _, name := range []string{processenv.RuntimeAddressEnvVar, localrpc.SessionTokenEnvVar} {
			if _, ok := unreached[name]; ok {
				t.Errorf("%s stated for an app with no runtime to reach", name)
			}
		}
	})

	t.Run("the app folder is always stated", func(t *testing.T) {
		t.Parallel()

		bound := resolvedEnv(nil, nil, nil, runtimeAccess{}, "/web", variables.Scope{})
		if bound[processenv.AppFolderEnvVar] != "/web" {
			t.Errorf("%s = %q, want %q", processenv.AppFolderEnvVar, bound[processenv.AppFolderEnvVar], "/web")
		}

		unbound := resolvedEnv(nil, nil, nil, runtimeAccess{}, "", variables.Scope{})
		folder, ok := unbound[processenv.AppFolderEnvVar]
		if !ok {
			t.Fatalf("resolvedEnv = %v, want %s written even for an unbound app", unbound, processenv.AppFolderEnvVar)
		}
		if folder != "" {
			t.Errorf("%s = %q, want the project root spelled as the empty string", processenv.AppFolderEnvVar, folder)
		}

		stale := toMap(applyEnv([]string{processenv.AppFolderEnvVar + "=/stale"}, resolvedEnv(nil, nil, nil, runtimeAccess{}, "", variables.Scope{})))
		if stale[processenv.AppFolderEnvVar] != "" {
			t.Errorf("%s = %q, want the shell's stale binding overwritten", processenv.AppFolderEnvVar, stale[processenv.AppFolderEnvVar])
		}

		contested := resolvedEnv(
			map[string]string{processenv.AppFolderEnvVar: "/from-live"},
			map[string]string{processenv.AppFolderEnvVar: "/from-dotfile"},
			[]binding.Resolved{{Name: "main", Env: map[string]string{processenv.AppFolderEnvVar: "/from-resource"}}},
			runtimeAccess{},
			"/web",
			variables.Scope{},
		)
		if contested[processenv.AppFolderEnvVar] != "/web" {
			t.Errorf("%s = %q, want the binding dev states to outrank every source it merges", processenv.AppFolderEnvVar, contested[processenv.AppFolderEnvVar])
		}
	})
}

func TestRunDevEnvironment(t *testing.T) {
	t.Run("the dotfile and the app folder reach the child", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

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

		var exitErr *exitcode.ExitError
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
			if env[processenv.AppFolderEnvVar] != "/web" {
				t.Errorf("%s = %q, want the folder the only app binds", processenv.AppFolderEnvVar, env[processenv.AppFolderEnvVar])
			}
		})

		t.Run("the notice accounts for every line of the file", func(t *testing.T) {
			if !strings.Contains(stderr.String(), "API_BASE") {
				t.Errorf("stderr = %q, want the divergence notice to name the key", stderr.String())
			}
			if !strings.Contains(stderr.String(), "line 5") {
				t.Errorf("stderr = %q, want the line that assigns nothing reported by number", stderr.String())
			}
			if strings.Contains(stderr.String(), "api_base") {
				t.Errorf("stderr = %q, want a line Ocel could never be asked for passed over in silence", stderr.String())
			}
			if !strings.Contains(stderr.String(), "NEXT_PUBLIC_SITE_URL") {
				t.Errorf("stderr = %q, want a declarable key accounted for", stderr.String())
			}
			if !strings.Contains(stderr.String(), valueLayers{{from: dotfile.FileName, file: true}, {from: dotfile.LocalFileName, file: true}}.advice(true)) {
				t.Errorf("stderr = %q, want the advice for a run that re-resolves on save", stderr.String())
			}
		})
	})

	t.Run("it refuses when the dotfile does not contain a required value", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"DATABASE_URL","class":"VARIABLE_CLASS_PLAIN","required":true}`))

		startedPath := filepath.Join(root, "started")
		appCmd := []string{"sh", "-c", "touch " + startedPath}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), deps, false, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		if err == nil {
			t.Fatal("runDev = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), dotfile.FileName) {
			t.Errorf("err = %q, want it to name DATABASE_URL and %s", err.Error(), dotfile.FileName)
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
		t.Cleanup(func() { _ = leader.Release(root) })

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
			t.Fatal("runDev = nil, want a refusal rather than a green variables check and a throw at the first read")
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
		t.Cleanup(func() { _ = leader.Release(root) })

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

		var exitErr *exitcode.ExitError
		if !errors.As(err, &exitErr) || exitErr.Code != 7 {
			t.Fatalf("runDev err = %v, want the app to have run and exited 7; stderr=%s", err, stderr.String())
		}
		if _, statErr := os.Stat(startedPath); statErr != nil {
			t.Errorf("the app was not started: %v", statErr)
		}
	})

	t.Run("a dev source's value satisfies the variables check without a dotfile", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		counted := filepath.Join(root, "reads")
		writeDevSource(t, root, `{ exec: { command: ["sh", "-c", "echo read >> `+counted+`; printf 'STRIPE_API_KEY=sk_from_source'"], format: "dotenv" } }`)
		clitest.WriteFile(t, filepath.Join(root, ".env"), "STRIPE_API_KEY=sk_from_dotenv\n")
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}`))

		env, reported := dumpDevEnv(t, devDeps(), root)
		if env["STRIPE_API_KEY"] != "sk_from_source" {
			t.Errorf("STRIPE_API_KEY = %q, want the dev source's value, and .env left unread under another source", env["STRIPE_API_KEY"])
		}
		if !strings.Contains(reported, "STRIPE_API_KEY") || !strings.Contains(reported, "exec") {
			t.Errorf("reported = %q, want it to say STRIPE_API_KEY came from exec", reported)
		}
		if strings.Contains(reported, "sk_from_source") {
			t.Errorf("reported = %q, want no value printed", reported)
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
		t.Cleanup(func() { _ = leader.Release(root) })

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
			t.Errorf("API_BASE = %q, ROOT_ONLY = %q, want /web's value over the root's, and the root's where /web has none", env["API_BASE"], env["ROOT_ONLY"])
		}
	})

	t.Run(dotfile.LocalFileName+" outranks the dev source", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		writeDevSource(t, root, `{ exec: { command: ["sh", "-c", "printf 'STRIPE_API_KEY=sk_from_source\\nLOG_LEVEL=info'"], format: "dotenv" } }`)
		clitest.WriteFile(t, filepath.Join(root, dotfile.LocalFileName), "STRIPE_API_KEY=sk_mine\n")
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}`))

		env, reported := dumpDevEnv(t, devDeps(), root)
		if env["STRIPE_API_KEY"] != "sk_mine" || env["LOG_LEVEL"] != "info" {
			t.Errorf("STRIPE_API_KEY = %q, LOG_LEVEL = %q, want %s over the source and the source beneath it", env["STRIPE_API_KEY"], env["LOG_LEVEL"], dotfile.LocalFileName)
		}
		if !strings.Contains(reported, dotfile.LocalFileName) {
			t.Errorf("reported = %q, want it to say what came from %s", reported, dotfile.LocalFileName)
		}
	})

	t.Run(dotfile.LocalFileName+" outranks .env under the default source", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		clitest.WriteFile(t, filepath.Join(root, dotfile.FileName), "STRIPE_API_KEY=sk_shared\nLOG_LEVEL=info\n")
		clitest.WriteFile(t, filepath.Join(root, dotfile.LocalFileName), "STRIPE_API_KEY=sk_mine\n")
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}`))

		env, _ := dumpDevEnv(t, devDeps(), root)
		if env["STRIPE_API_KEY"] != "sk_mine" || env["LOG_LEVEL"] != "info" {
			t.Errorf("STRIPE_API_KEY = %q, LOG_LEVEL = %q, want %s over %s", env["STRIPE_API_KEY"], env["LOG_LEVEL"], dotfile.LocalFileName, dotfile.FileName)
		}
	})

	t.Run("a dev source that cannot be read stops the run and says why", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		writeDevSource(t, root, `{ exec: { command: ["sh", "-c", "echo 'vault is sealed' >&2; exit 3"], format: "json" } }`)
		startedPath := filepath.Join(root, "started")

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), devDeps(), false, root, []string{"sh", "-c", "touch " + startedPath}, &stdout, &stderr, strings.NewReader(""))
		if err == nil || !strings.Contains(err.Error(), "exit status 3") || strings.Contains(err.Error(), "vault is sealed") {
			t.Fatalf("runDev err = %v, want how the source's command exited and never what it printed", err)
		}
		if _, statErr := os.Stat(startedPath); statErr == nil {
			t.Error("the app was started without its dev source")
		}
	})

	t.Run("an Infisical dev source with no way in says how to sign in", func(t *testing.T) {
		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		clitest.WriteFile(t, filepath.Join(root, project.DefaultFileName), `{"slug":"test-app","envSource":{"dev":{"infisical":{"project":"p-1","environment":"dev"}}}}`)
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
		t.Cleanup(func() { _ = leader.Release(root) })

		t.Setenv("INFISICAL_TOKEN", "developer-token")
		writeDevSource(t, root, `{ infisical: { project: "p-1", environment: "dev", host: "`+server.URL+`" } }`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}`))

		env, reported := dumpDevEnv(t, devDeps(), root)
		if env["STRIPE_API_KEY"] != "sk_from_infisical" {
			t.Errorf("STRIPE_API_KEY = %q, want Infisical's value", env["STRIPE_API_KEY"])
		}
		if !strings.Contains(reported, "infisical:p-1/dev") {
			t.Errorf("reported = %q, want it to name the Infisical source", reported)
		}
	})

	t.Run("a live-class key is not refused for having no local value", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"DB_PASSWORD","class":"VARIABLE_CLASS_SECRET","required":true}`))

		startedPath := filepath.Join(root, "started")
		appCmd := []string{"sh", "-c", "touch " + startedPath + "; exit 7"}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), deps, false, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitcode.ExitError
		if !errors.As(err, &exitErr) || exitErr.Code != 7 {
			t.Fatalf("runDev err = %v, want exit 7 (no refusal); stderr=%s", err, stderr.String())
		}
		if _, statErr := os.Stat(startedPath); statErr != nil {
			t.Fatalf("the app was not started: %v", statErr)
		}
		if !strings.Contains(stderr.String(), "DB_PASSWORD") {
			t.Errorf("stderr = %q, want the live-value notice to name DB_PASSWORD", stderr.String())
		}
	})

	t.Run("it generates the client accessor and exports the value under its declared name", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(root, "package.json"), "{}\n")
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

		var exitErr *exitcode.ExitError
		if !errors.As(err, &exitErr) || exitErr.Code != 7 {
			t.Fatalf("runDev err = %v, want exit 7 (no refusal); stderr=%s", err, stderr.String())
		}

		accessor, readErr := os.ReadFile(filepath.Join(root, statedir.Name, "env-client.ts"))
		if readErr != nil {
			t.Fatalf("dev generated no client accessor: %v", readErr)
		}
		if !strings.Contains(string(accessor), `PUBLIC_SITE_URL: inlined(schema, "PUBLIC_SITE_URL", process.env.PUBLIC_SITE_URL)`) {
			t.Errorf("accessor = %s, want it to read the key under its declared name", accessor)
		}
		if strings.Contains(string(accessor), "STRIPE_API_KEY") {
			t.Errorf("accessor names a server-only value:\n%s", accessor)
		}
		if tsconfig := readTestFile(t, filepath.Join(root, "tsconfig.json")); !strings.Contains(tsconfig, `"ocel/env/client": ["./`+statedir.Name+`/env-client.ts"]`) {
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
		t.Cleanup(func() { _ = leader.Release(root) })

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

		var exitErr *exitcode.ExitError
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
		t.Cleanup(func() { _ = leader.Release(root) })

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

		var exitErr *exitcode.ExitError
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
		if env[processenv.AppFolderEnvVar] != "/web" {
			t.Errorf("%s = %q, want the folder the only app binds", processenv.AppFolderEnvVar, env[processenv.AppFolderEnvVar])
		}
		if !strings.Contains(stderr.String(), valueLayers{{from: dotfile.FileName, file: true}, {from: dotfile.LocalFileName, file: true}}.advice(false)) {
			t.Errorf("stderr = %q, want the advice for a run that reads the file once", stderr.String())
		}
	})

	t.Run("it refuses when the dotfile does not contain a required value", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"DATABASE_URL","class":"VARIABLE_CLASS_PLAIN","required":true}`))

		startedPath := filepath.Join(root, "started")
		appCmd := []string{"sh", "-c", "touch " + startedPath}

		var stdout, stderr bytes.Buffer
		err := runRun(context.Background(), deps, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		if err == nil {
			t.Fatal("runRun = nil, want the same refusal `ocel dev` gives")
		}
		if !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), dotfile.FileName) {
			t.Errorf("err = %q, want it to name DATABASE_URL and %s", err.Error(), dotfile.FileName)
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
		t.Cleanup(func() { _ = leader.Release(root) })

		writeDevSource(t, root, `{ exec: { command: ["sh", "-c", "printf '{\"STRIPE_API_KEY\":\"sk_from_source\"}'"], format: "json" } }`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}`))

		envDumpPath := filepath.Join(root, "env.out")
		var stdout, stderr syncBuffer
		err := runRun(context.Background(), devDeps(), root, []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitcode.ExitError
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

func dumpDevEnv(t *testing.T, deps testDeps, root string) (map[string]string, string) {
	t.Helper()
	envDumpPath := filepath.Join(root, "env.out")
	var stdout, stderr syncBuffer
	err := runDev(context.Background(), deps, false, root, []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}, &stdout, &stderr, strings.NewReader(""))
	var exitErr *exitcode.ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("runDev err = %v, want exit 7 (no refusal); stderr=%s", err, stderr.String())
	}
	return toMap(strings.Split(strings.TrimRight(readTestFile(t, envDumpPath), "\n"), "\n")), stderr.String()
}

func TestDevGivesEveryAppItsURL(t *testing.T) {
	node := variables.Scope{Apps: []variables.App{{Name: "web", ClientBundle: true}}}

	t.Run("localhost on the default port where nothing names one", func(t *testing.T) {
		t.Setenv("PORT", "")

		got := resolvedEnv(nil, nil, nil, runtimeAccess{}, "", node)
		for _, key := range []string{processenv.AppURLEnvVar, processenv.ClientURLEnvVar} {
			if want := "http://localhost:3000"; got[key] != want {
				t.Errorf("%s = %q, want %q — dev never leaves it unset, so an app may read it without a fallback", key, got[key], want)
			}
		}
	})

	t.Run("the browser's copy only where an app's bundle reads it", func(t *testing.T) {
		t.Setenv("PORT", "")
		dotfile := map[string]string{processenv.ClientURLEnvVar: "https://mine.example"}

		got := resolvedEnv(nil, dotfile, nil, runtimeAccess{}, "", variables.Scope{Apps: []variables.App{{Name: "api"}}})
		if want := "http://localhost:3000"; got[processenv.AppURLEnvVar] != want {
			t.Errorf("%s = %q, want %q for every app", processenv.AppURLEnvVar, got[processenv.AppURLEnvVar], want)
		}
		if want := "https://mine.example"; got[processenv.ClientURLEnvVar] != want {
			t.Errorf("%s = %q, want the go app's own %q: nothing in a go app reads ocel's copy, so writing one overwrites its value", processenv.ClientURLEnvVar, got[processenv.ClientURLEnvVar], want)
		}
	})

	t.Run("the port the project names", func(t *testing.T) {
		t.Setenv("PORT", "")

		got := resolvedEnv(nil, map[string]string{"PORT": "4321"}, nil, runtimeAccess{}, "", node)
		if want := "http://localhost:4321"; got[processenv.AppURLEnvVar] != want {
			t.Errorf("%s = %q, want %q", processenv.AppURLEnvVar, got[processenv.AppURLEnvVar], want)
		}
	})

	t.Run("the port the shell exports", func(t *testing.T) {
		t.Setenv("PORT", "8080")

		got := resolvedEnv(nil, nil, nil, runtimeAccess{}, "", node)
		if want := "http://localhost:8080"; got[processenv.AppURLEnvVar] != want {
			t.Errorf("%s = %q, want %q — the app is spawned with the shell's environment under it", processenv.AppURLEnvVar, got[processenv.AppURLEnvVar], want)
		}
	})
}

func TestRunWritesTheBrowsersURLForTheAppItRunsIn(t *testing.T) {
	t.Setenv("PORT", "")
	root := t.TempDir()
	cfg := &project.Project{Dir: root, Apps: []project.App{
		{Name: "web", Path: filepath.Join("apps", "web"), Folder: "/web", Serverless: &project.Serverless{Framework: buildoutput.FrameworkNext}},
		{Name: "api", Path: filepath.Join("apps", "api"), Folder: "/api", Serverless: &project.Serverless{Framework: buildoutput.FrameworkGo}},
		{Name: "webhooks", Path: filepath.Join("apps", "web-hooks"), Serverless: &project.Serverless{Framework: buildoutput.FrameworkPython}},
		{Name: "store", Path: filepath.Join("apps", "store"), Folder: "/store", Compute: provider.ComputeContainer},
		{Name: "worker", Path: filepath.Join("apps", "worker"), Folder: "/worker", Compute: provider.ComputeContainer},
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
		{name: "inside a container app whose directory contains a package.json", cwd: filepath.Join(root, "apps", "store"), written: true},
		{name: "inside a container app whose directory contains a go.mod", cwd: filepath.Join(root, "apps", "worker"), written: false},
		{name: "at the project root, where no one app is the target", cwd: root, written: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvedEnv(nil, nil, nil, runtimeAccess{}, "", targetScope(cfg, tc.cwd))
			if _, written := got[processenv.ClientURLEnvVar]; written != tc.written {
				t.Errorf("%s written = %v, want %v — a command run inside one app reads only that app's runtime, and elsewhere any app in the project", processenv.ClientURLEnvVar, written, tc.written)
			}
		})
	}
}

func TestDevNeedsNoValueADeployedTierAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	for _, command := range []struct {
		name string
		run  func(root string, appCmd []string, stdout, stderr io.Writer) error
	}{
		{"dev", func(root string, appCmd []string, stdout, stderr io.Writer) error {
			return runDev(context.Background(), devDeps(), false, root, appCmd, stdout, stderr, strings.NewReader(""))
		}},
		{"run", func(root string, appCmd []string, stdout, stderr io.Writer) error {
			return runRun(context.Background(), devDeps(), root, appCmd, stdout, stderr, strings.NewReader(""))
		}},
	} {
		t.Run(command.name+" starts without a production binding's variables or env source credentials", func(t *testing.T) {
			root := t.TempDir()
			t.Cleanup(func() { _ = leader.Release(root) })

			clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  bindings: { postgres: { main: { url: { $env: "MAIN_DATABASE_URL" } } } },
  envSource: {
    production: { infisical: { project: "p-1", environment: "prod", auth: { universal: { clientId: { $env: "INFISICAL_CLIENT_ID" }, clientSecret: { $env: "INFISICAL_CLIENT_SECRET" } } } } },
  },
};
`)
			clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"LOG_LEVEL","class":"VARIABLE_CLASS_PLAIN"}`))

			var stdout, stderr syncBuffer
			err := command.run(root, []string{"sh", "-c", "exit 7"}, &stdout, &stderr)

			var exitErr *exitcode.ExitError
			if !errors.As(err, &exitErr) || exitErr.Code != 7 {
				t.Fatalf("err = %v, want the app started and its exit 7 passed through; stderr=%s", err, stderr.String())
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

func TestTheChildEnvironment(t *testing.T) {
	t.Parallel()

	t.Run("a resource outranks the project, and the project outranks the shell", func(t *testing.T) {
		t.Parallel()

		base := []string{"PATH=/bin", "SHARED=base"}
		values := map[string]string{"SHARED": "project", "PROJECT_ONLY": "p"}
		resources := []binding.Resolved{
			{Name: "main", Env: map[string]string{"SHARED": "resource", "OCEL_RESOURCE_POSTGRES_main": "conn"}},
		}

		got := toMap(applyEnv(base, resolvedEnv(nil, values, resources, runtimeAccess{}, "", variables.Scope{})))

		cases := map[string]string{
			"PATH":                        "/bin",
			"SHARED":                      "resource",
			"PROJECT_ONLY":                "p",
			"OCEL_RESOURCE_POSTGRES_main": "conn",
		}
		for k, want := range cases {
			if got[k] != want {
				t.Errorf("env[%q] = %q, want %q", k, got[k], want)
			}
		}
	})

	t.Run("dev never tells the runtime to wait for a push", func(t *testing.T) {
		t.Parallel()

		live := map[string]string{"WEBHOOK_SECRET": "whsec_live"}

		got := applyEnv([]string{"PATH=/usr/bin"}, resolvedEnv(live, map[string]string{"PROJECT_ONLY": "p"}, nil, runtimeAccess{}, "", variables.Scope{}))

		for _, kv := range got {
			if strings.HasPrefix(kv, "OCEL_LIVE_KEYS=") {
				t.Errorf("dev set %q; there is no runtime here to send the push it promises", kv)
			}
		}
		if !slices.Contains(got, "WEBHOOK_SECRET=whsec_live") {
			t.Error("the live value did not reach the child's environment under its bare name, which is dev's only delivery")
		}
	})
}
