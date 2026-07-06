package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/internal/credentials"
	"github.com/ocelhq/ocel/internal/devserver"
	"github.com/ocelhq/ocel/internal/lockfile"
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
	err := runDev(context.Background(), t.TempDir(), []string{"true"}, &bytes.Buffer{}, &stderr, strings.NewReader(""))

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
	err := runDev(context.Background(), root, appCmd, &stdout, &stderr, strings.NewReader(""))

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

func TestRunDev_SecondRunForSameProject_BecomesFollowerAndReceivesPushedEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	prev := loadCredentials
	loadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{APIURL: "https://api.example.com", AccessToken: "tok"}, nil
	}
	defer func() { loadCredentials = prev }()

	projectID := "proj_" + t.Name()
	t.Cleanup(func() { _ = lockfile.Remove(projectID) })

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), fmt.Sprintf(`
export default {
  projectId: %q,
};
`, projectID))
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

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()

	leaderDone := make(chan error, 1)
	var leaderStdout, leaderStderr bytes.Buffer
	go func() {
		// A bare "sleep" (not "sh -c sleep 10") so ctx cancellation's
		// Process.Kill() actually stops it directly, rather than killing a
		// forking shell and leaving a "sleep" grandchild running.
		leaderDone <- runDev(leaderCtx, root, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
	}()

	waitForLockfile(t, projectID)

	envDumpPath := filepath.Join(root, "follower-env.out")
	followerAppArgs := []string{"sh", "-c", "env > " + envDumpPath + "; exit 9"}

	var followerStdout, followerStderr bytes.Buffer
	err := runDev(context.Background(), root, followerAppArgs, &followerStdout, &followerStderr, strings.NewReader(""))

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("follower runDev err = %v, want *ExitError; stderr=%s", err, followerStderr.String())
	}
	if exitErr.Code != 9 {
		t.Fatalf("follower ExitError.Code = %d, want 9", exitErr.Code)
	}

	dumped, err := os.ReadFile(envDumpPath)
	if err != nil {
		t.Fatalf("read follower env dump: %v", err)
	}
	env := toMap(strings.Split(strings.TrimRight(string(dumped), "\n"), "\n"))

	raw, ok := env["OCEL_RESOURCE_POSTGRES_main"]
	if !ok {
		t.Fatalf("follower env missing OCEL_RESOURCE_POSTGRES_main, got: %s", dumped)
	}
	if !strings.Contains(raw, "connectionString") {
		t.Fatalf("OCEL_RESOURCE_POSTGRES_main = %q, want it to contain connectionString", raw)
	}

	cancelLeader()
	select {
	case <-leaderDone:
	case <-time.After(5 * time.Second):
		t.Fatal("leader runDev did not exit after cancellation")
	}
}

func TestRunDev_Follower_LeaderDisconnects_StopsChildPrintsMessageAndExitsNonZero(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	prev := loadCredentials
	loadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{APIURL: "https://api.example.com", AccessToken: "tok"}, nil
	}
	defer func() { loadCredentials = prev }()

	projectID := "proj_" + t.Name()
	t.Cleanup(func() { _ = lockfile.Remove(projectID) })

	srv := devserver.New("https://api.example.com", "tok", projectID)
	srv.PushEnv(map[string]string{"OCEL_RESOURCE_POSTGRES_main": `{"connectionString":"conn"}`})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	httpSrv := &http.Server{Handler: srv.Mux()}
	go httpSrv.Serve(listener)

	if err := lockfile.Write(projectID, listener.Addr().String()); err != nil {
		t.Fatalf("lockfile.Write: %v", err)
	}

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), fmt.Sprintf(`
export default {
  projectId: %q,
};
`, projectID))

	startedPath := filepath.Join(root, "started")
	appArgs := []string{"sh", "-c", "touch " + startedPath + "; sleep 10"}

	followerDone := make(chan error, 1)
	var stdout, stderr bytes.Buffer
	go func() {
		followerDone <- runDev(context.Background(), root, appArgs, &stdout, &stderr, strings.NewReader(""))
	}()

	waitForFile(t, startedPath)

	if err := httpSrv.Close(); err != nil {
		t.Fatalf("close fake leader: %v", err)
	}

	select {
	case err := <-followerDone:
		var exitErr *ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("follower runDev err = %v, want *ExitError; stderr=%s", err, stderr.String())
		}
		if exitErr.Code == 0 {
			t.Fatalf("follower ExitError.Code = 0, want non-zero")
		}
		if !strings.Contains(stderr.String(), "Restart") {
			t.Fatalf("stderr = %q, want it to mention restarting the leader", stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follower runDev did not exit after leader disconnect")
	}
}

// waitForLockfile polls until projectID's leader lockfile exists.
func waitForLockfile(t *testing.T, projectID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := lockfile.Read(projectID); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("lockfile for %q never appeared", projectID)
}

// waitForFile polls until path exists.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%q never appeared", path)
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
