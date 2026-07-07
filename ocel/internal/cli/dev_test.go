package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/internal/credentials"
	"github.com/ocelhq/ocel/internal/provision"
)

func TestMergeEnv_Precedence(t *testing.T) {
	base := []string{"PATH=/bin", "SHARED=base"}
	projectEnv := map[string]string{"SHARED": "project", "PROJECT_ONLY": "p"}
	resources := []provision.ProvisionedResource{
		{Name: "main", Env: map[string]string{"SHARED": "resource", "OCEL_RESOURCE_POSTGRES_main": "conn"}},
	}

	got := toMap(mergeEnv(base, projectEnv, resources))

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
}

func toMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	return m
}

func TestRunDev_NotLoggedIn_ReturnsExitErrorWithLoginInstruction(t *testing.T) {
	prev := loadCredentials
	loadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{}, credentials.ErrNotLoggedIn
	}
	defer func() { loadCredentials = prev }()

	var stderr bytes.Buffer
	err := runDev(context.Background(), t.TempDir(), []string{"true"}, false, &bytes.Buffer{}, &stderr, strings.NewReader(""))

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runDev err = %v (%T), want *ExitError", err, err)
	}
	if exitErr.Code == 0 {
		t.Fatalf("ExitError.Code = 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "ocel login") {
		t.Fatalf("stderr = %q, want it to mention `ocel login`", stderr.String())
	}
}

func TestRunDev_HappyPath_DiscoversDeclaresSyncsAndSpawnsWithExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	prev := loadCredentials
	loadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{APIURL: "https://api.example.com", AccessToken: "tok"}, nil
	}
	defer func() { loadCredentials = prev }()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  projectId: "proj_123",
};
`)
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), `
declare global {
  var __ocelRegister: Promise<unknown>[];
}
globalThis.__ocelRegister ??= [];
globalThis.__ocelRegister.push(
  fetch(new URL("/resources.v1.ResourceService/Declare", process.env.OCEL_DEV_SERVER), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      resource: { type: "RESOURCE_TYPE_POSTGRES", name: "main" },
      postgres: { version: "17" },
    }),
  }),
);
export {};
`)

	envDumpPath := filepath.Join(root, "env.out")
	appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

	var stdout, stderr bytes.Buffer
	err := runDev(context.Background(), root, appCmd, false, &stdout, &stderr, strings.NewReader(""))

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runDev err = %v, want *ExitError; stderr=%s", err, stderr.String())
	}
	if exitErr.Code != 7 {
		t.Fatalf("ExitError.Code = %d, want 7", exitErr.Code)
	}

	dumped, err := os.ReadFile(envDumpPath)
	if err != nil {
		t.Fatalf("read env dump: %v", err)
	}
	env := toMap(strings.Split(strings.TrimRight(string(dumped), "\n"), "\n"))

	raw, ok := env["OCEL_RESOURCE_POSTGRES_main"]
	if !ok {
		t.Fatalf("app env missing OCEL_RESOURCE_POSTGRES_main, got: %s", dumped)
	}
	if !strings.Contains(raw, "connectionString") {
		t.Fatalf("OCEL_RESOURCE_POSTGRES_main = %q, want it to contain connectionString", raw)
	}
}

func TestDevCmd_LocalHarnessFlag_IsHidden(t *testing.T) {
	flag := devCmd.Flags().Lookup("local-harness")
	if flag == nil {
		t.Fatal("devCmd has no --local-harness flag")
	}
	if !flag.Hidden {
		t.Fatal("--local-harness flag is not hidden")
	}
}

// TestRunDev_LocalHarness_RoutesProvisioningAndStopsHarnessBeforeApp backs
// the hidden --local-harness flag with an httptest server (via the
// startLocalHarness seam) serving the same /dev/project-config and
// /dev/provision routes as apps/web/scripts/local-api-server.ts, and checks
// that provisioning routes through it and that the harness is torn down
// before the app command starts.
func TestRunDev_LocalHarness_RoutesProvisioningAndStopsHarnessBeforeApp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	prevCreds := loadCredentials
	loadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{APIURL: "https://api.example.com", AccessToken: "tok"}, nil
	}
	defer func() { loadCredentials = prevCreds }()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  projectId: "proj_123",
};
`)
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), `
declare global {
  var __ocelRegister: Promise<unknown>[];
}
globalThis.__ocelRegister ??= [];
globalThis.__ocelRegister.push(
  fetch(new URL("/resources.v1.ResourceService/Declare", process.env.OCEL_DEV_SERVER), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      resource: { type: "RESOURCE_TYPE_POSTGRES", name: "main" },
      postgres: { version: "17" },
    }),
  }),
);
export {};
`)

	var configCalls, provisionCalls int
	harness := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dev/project-config":
			configCalls++
			json.NewEncoder(w).Encode(map[string]any{
				"orgId":     "org_harness",
				"projectId": "proj_123",
				"userId":    "user_harness",
				"envVars":   map[string]string{"FROM_HARNESS": "1"},
			})
		case "/dev/provision":
			provisionCalls++
			json.NewEncoder(w).Encode([]map[string]any{{
				"name": "main",
				"type": "POSTGRES",
				"env": map[string]string{
					"OCEL_RESOURCE_POSTGRES_main": `{"connectionString":"postgres://harness"}`,
				},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer harness.Close()

	stopMarker := filepath.Join(root, "harness-stopped")
	var spawnedDir string
	prevSpawn := startLocalHarness
	startLocalHarness = func(_ context.Context, projectDir string) (string, func(), error) {
		spawnedDir = projectDir
		stop := func() {
			writeFile(t, stopMarker, "stopped")
			harness.Close()
		}
		return strings.TrimPrefix(harness.URL, "http://"), stop, nil
	}
	defer func() { startLocalHarness = prevSpawn }()

	// The app command records whether the harness had already been stopped
	// by the time it started, then dumps its environment.
	envDumpPath := filepath.Join(root, "env.out")
	seenPath := filepath.Join(root, "stopped-at-app-start")
	appCmd := []string{"sh", "-c", "cp " + stopMarker + " " + seenPath + " 2>/dev/null; env > " + envDumpPath}

	var stdout, stderr bytes.Buffer
	if err := runDev(context.Background(), root, appCmd, true, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDev err = %v; stderr=%s", err, stderr.String())
	}

	if spawnedDir != root {
		t.Errorf("harness spawned in %q, want project dir %q", spawnedDir, root)
	}
	if configCalls != 1 || provisionCalls != 1 {
		t.Errorf("harness calls: project-config=%d provision=%d, want 1 and 1", configCalls, provisionCalls)
	}
	if _, err := os.Stat(seenPath); err != nil {
		t.Errorf("harness was not stopped before the app command started: %v", err)
	}

	dumped, err := os.ReadFile(envDumpPath)
	if err != nil {
		t.Fatalf("read env dump: %v", err)
	}
	env := toMap(strings.Split(strings.TrimRight(string(dumped), "\n"), "\n"))
	if env["FROM_HARNESS"] != "1" {
		t.Errorf("app env FROM_HARNESS = %q, want %q (project config not routed through harness)", env["FROM_HARNESS"], "1")
	}
	if got := env["OCEL_RESOURCE_POSTGRES_main"]; !strings.Contains(got, "postgres://harness") {
		t.Errorf("OCEL_RESOURCE_POSTGRES_main = %q, want harness-provisioned connection string", got)
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
