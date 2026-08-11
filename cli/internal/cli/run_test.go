package cli

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
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

func TestRunRun(t *testing.T) {
	t.Run("not logged in returns an exit error pointing at `ocel login`", func(t *testing.T) {
		d := defaultDeps()
		d.loadCredentials = func() (credentials.Credentials, error) {
			return credentials.Credentials{}, credentials.ErrNotLoggedIn
		}

		var stderr bytes.Buffer
		err := runRun(context.Background(), d, nil, t.TempDir(), []string{"true"}, &bytes.Buffer{}, &stderr, strings.NewReader(""))

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
	})

	t.Run("with no leader it stands alone, resolves, runs and tears down", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		d := defaultDeps()
		withCredentials(&d, resolveServer.URL)

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		writeLink(t, root, resolveServer.URL, testProjectID(t))
		writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareResourceScript("main"))

		envDumpPath := filepath.Join(root, "env.out")
		appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

		var stdout, stderr bytes.Buffer
		err := runRun(context.Background(), d, nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		t.Run("the child's exit code becomes the command's", func(t *testing.T) {
			var exitErr *ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("runRun err = %v, want *ExitError; stderr=%s", err, stderr.String())
			}
			if exitErr.Code != 7 {
				t.Fatalf("ExitError.Code = %d, want 7", exitErr.Code)
			}
		})

		t.Run("the resolved resource reaches the child's environment", func(t *testing.T) {
			dumped, readErr := os.ReadFile(envDumpPath)
			if readErr != nil {
				t.Fatalf("read env dump: %v", readErr)
			}
			env := toMap(strings.Split(strings.TrimRight(string(dumped), "\n"), "\n"))

			raw, ok := env["OCEL_RESOURCE_POSTGRES_main"]
			if !ok {
				t.Fatalf("app env missing OCEL_RESOURCE_POSTGRES_main, got: %s", dumped)
			}
			if !strings.Contains(raw, "connectionString") {
				t.Fatalf("OCEL_RESOURCE_POSTGRES_main = %q, want it to contain connectionString", raw)
			}
		})

		t.Run("it leaves no lockfile behind, having never advertised itself as leader", func(t *testing.T) {
			if _, err := lockfile.Read(root); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("lockfile.Read err = %v, want a not-exist error (ocel run must not advertise as leader)", err)
			}
		})
	})

	t.Run("with a running leader it reuses the leader's env and runs once", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		d := defaultDeps()
		withCredentials(&d, resolveServer.URL)

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		writeLink(t, root, resolveServer.URL, testProjectID(t))
		writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareResourceScript("main"))

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()

		leaderDone := make(chan error, 1)
		var leaderStdout, leaderStderr syncBuffer
		go func() {
			leaderDone <- runDev(leaderCtx, d, nil, root, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
		}()

		waitForLockfile(t, root)

		envDumpPath := filepath.Join(root, "run-env.out")
		runAppArgs := []string{"sh", "-c", "env > " + envDumpPath + "; exit 9"}

		var stdout, stderr bytes.Buffer
		err := runRun(context.Background(), d, nil, root, runAppArgs, &stdout, &stderr, strings.NewReader(""))

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
	})

	t.Run("with a running leader it waits on neither follower updates nor a disconnect", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		d := defaultDeps()
		setLoggedIn(&d)

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		projectID := testProjectID(t)
		const apiURL = "https://api.example.com"
		srv := devserver.New(apiURL, "tok", projectID, "http://127.0.0.1:0")
		srv.PushEnv(map[string]string{"OCEL_RESOURCE_POSTGRES_main": `{"connectionString":"conn"}`})

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		httpSrv := &http.Server{Handler: srv.Mux()}
		go httpSrv.Serve(listener)
		defer httpSrv.Close()

		if err := lockfile.Create(root, listener.Addr().String()); err != nil {
			t.Fatalf("lockfile.Write: %v", err)
		}

		writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		writeLink(t, root, apiURL, projectID)

		var stdout, stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- runRun(context.Background(), d, nil, root, []string{"true"}, &stdout, &stderr, strings.NewReader(""))
		}()

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("runRun err = %v, want nil (command exited 0); stderr=%s", err, stderr.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("runRun did not return promptly for a one-off command against a live leader")
		}
	})
}
