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

	"github.com/ocelhq/ocel/cli/internal/devlock"
	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/devstack"
	"github.com/ocelhq/ocel/cli/internal/exitsig"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestRunRun(t *testing.T) {
	t.Run("with no leader it stands alone, resolves, runs and tears down", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		deps := devDeps()
		withCredentials(&deps, testAPIURL)

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		writeLink(t, root, testAPIURL, testProjectID(t))
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		envDumpPath := filepath.Join(root, "env.out")
		appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

		var stdout, stderr bytes.Buffer
		err := runRun(context.Background(), deps, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		t.Run("the child's exit code becomes the command's", func(t *testing.T) {
			var exitErr *exitsig.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("runRun err = %v, want *exitsig.ExitError; stderr=%s", err, stderr.String())
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
			if !strings.Contains(raw, `"postgres"`) {
				t.Fatalf("OCEL_RESOURCE_POSTGRES_main = %q, want it to carry a postgres link", raw)
			}
		})

		t.Run("it leaves no lockfile behind, having never advertised itself as leader", func(t *testing.T) {
			if _, err := devlock.Read(root); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("devlock.Read err = %v, want a not-exist error (ocel run must not advertise as leader)", err)
			}
		})
	})

	t.Run("with a running leader it reuses the leader's env and runs once", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		deps := devDeps()
		withCredentials(&deps, testAPIURL)

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		writeLink(t, root, testAPIURL, testProjectID(t))
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()

		leaderDone := make(chan error, 1)
		var leaderStdout, leaderStderr syncBuffer
		go func() {
			leaderDone <- runDev(leaderCtx, deps, false, root, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
		}()

		waitForLockfile(t, root)

		envDumpPath := filepath.Join(root, "run-env.out")
		runAppArgs := []string{"sh", "-c", "env > " + envDumpPath + "; exit 9"}

		var stdout, stderr bytes.Buffer
		err := runRun(context.Background(), deps, root, runAppArgs, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitsig.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("runRun err = %v, want *exitsig.ExitError; stderr=%s", err, stderr.String())
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
		if !strings.Contains(raw, `"postgres"`) {
			t.Fatalf("OCEL_RESOURCE_POSTGRES_main = %q, want it to carry a postgres link", raw)
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

		deps := devDeps()
		clitest.SetLoggedIn(&deps)

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		projectID := testProjectID(t)
		const apiURL = "https://api.example.com"
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		srv := devserver.New("http://"+listener.Addr().String(), devstack.New("a-leader", devstack.Env{}))
		srv.PushEnv(map[string]string{"OCEL_RESOURCE_POSTGRES_main": `{"name":"main","postgres":{"host":"resolved","port":5432,"database":"main","username":"u","password":"p"}}`})

		httpSrv := &http.Server{Handler: srv.Mux()}
		go httpSrv.Serve(listener)
		defer httpSrv.Close()

		if err := devlock.Create(root, devlock.Lease{Addr: listener.Addr().String(), Token: srv.AppToken()}); err != nil {
			t.Fatalf("devlock.Write: %v", err)
		}

		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		writeLink(t, root, apiURL, projectID)

		var stdout, stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- runRun(context.Background(), deps, root, []string{"true"}, &stdout, &stderr, strings.NewReader(""))
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
