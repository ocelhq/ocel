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

	"github.com/ocelhq/ocel/cli/internal/credentials"
	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/lockfile"
)

func TestRunRun_NotLoggedIn_ReturnsExitErrorWithLoginInstruction(t *testing.T) {
	prev := loadCredentials
	loadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{}, credentials.ErrNotLoggedIn
	}
	defer func() { loadCredentials = prev }()

	var stderr bytes.Buffer
	err := runRun(context.Background(), nil, t.TempDir(), []string{"true"}, &bytes.Buffer{}, &stderr, strings.NewReader(""))

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runRun err = %v (%T), want *ExitError", err, err)
	}
	if exitErr.Code == 0 {
		t.Fatalf("ExitError.Code = 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "ocel login") {
		t.Fatalf("stderr = %q, want it to mention `ocel login`", stderr.String())
	}
}

func TestRunRun_NoLeader_StandaloneResolvesRunsAndTearsDownWithoutLockfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	resolveServer := newFakeResolveServer(t)
	defer resolveServer.Close()

	prev := loadCredentials
	loadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{APIURL: resolveServer.URL, AccessToken: "tok"}, nil
	}
	defer func() { loadCredentials = prev }()

	projectID := "proj_" + t.Name()
	t.Cleanup(func() { _ = lockfile.Remove(projectID) })

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), fmt.Sprintf(`
export default {
  slug: "test-app",
  projectId: %q,
};
`, projectID))
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareResourceScript("main"))

	envDumpPath := filepath.Join(root, "env.out")
	appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

	var stdout, stderr bytes.Buffer
	err := runRun(context.Background(), nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runRun err = %v, want *ExitError; stderr=%s", err, stderr.String())
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

	if _, err := lockfile.Read(projectID); !os.IsNotExist(err) {
		t.Fatalf("lockfile.Read err = %v, want a not-exist error (ocel run must not advertise as leader)", err)
	}
}

func TestRunRun_WithRunningLeader_ReusesLeaderEnvAndRunsOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	resolveServer := newFakeResolveServer(t)
	defer resolveServer.Close()

	prev := loadCredentials
	loadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{APIURL: resolveServer.URL, AccessToken: "tok"}, nil
	}
	defer func() { loadCredentials = prev }()

	projectID := "proj_" + t.Name()
	t.Cleanup(func() { _ = lockfile.Remove(projectID) })

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), fmt.Sprintf(`
export default {
  slug: "test-app",
  projectId: %q,
};
`, projectID))
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareResourceScript("main"))

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()

	leaderDone := make(chan error, 1)
	var leaderStdout, leaderStderr bytes.Buffer
	go func() {
		leaderDone <- runDev(leaderCtx, nil, root, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
	}()

	waitForLockfile(t, projectID)

	envDumpPath := filepath.Join(root, "run-env.out")
	runAppArgs := []string{"sh", "-c", "env > " + envDumpPath + "; exit 9"}

	var stdout, stderr bytes.Buffer
	err := runRun(context.Background(), nil, root, runAppArgs, &stdout, &stderr, strings.NewReader(""))

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runRun err = %v, want *ExitError; stderr=%s", err, stderr.String())
	}
	if exitErr.Code != 9 {
		t.Fatalf("ExitError.Code = %d, want 9", exitErr.Code)
	}

	dumped, err := os.ReadFile(envDumpPath)
	if err != nil {
		t.Fatalf("read run env dump: %v", err)
	}
	env := toMap(strings.Split(strings.TrimRight(string(dumped), "\n"), "\n"))

	raw, ok := env["OCEL_RESOURCE_POSTGRES_main"]
	if !ok {
		t.Fatalf("run env missing OCEL_RESOURCE_POSTGRES_main, got: %s", dumped)
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

// TestRunRun_WithRunningLeader_DoesNotWaitOnFollowerUpdatesOrDisconnect
// verifies that ocel run's follower path returns as soon as the one-off
// command exits, even though the leader (and its Subscribe stream) is still
// alive — i.e. it does not loop waiting for further pushes the way `ocel dev`
// followers do.
func TestRunRun_WithRunningLeader_DoesNotWaitOnFollowerUpdatesOrDisconnect(t *testing.T) {
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

	srv := devserver.New("https://api.example.com", "tok", projectID, "http://127.0.0.1:0")
	srv.PushEnv(map[string]string{"OCEL_RESOURCE_POSTGRES_main": `{"connectionString":"conn"}`})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	httpSrv := &http.Server{Handler: srv.Mux()}
	go httpSrv.Serve(listener)
	defer httpSrv.Close()

	if err := lockfile.Create(projectID, listener.Addr().String()); err != nil {
		t.Fatalf("lockfile.Write: %v", err)
	}

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), fmt.Sprintf(`
export default {
  slug: "test-app",
  projectId: %q,
};
`, projectID))

	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runRun(context.Background(), nil, root, []string{"true"}, &stdout, &stderr, strings.NewReader(""))
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runRun err = %v, want nil (command exited 0); stderr=%s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runRun did not return promptly for a one-off command against a live leader")
	}
}
