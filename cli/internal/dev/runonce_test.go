package dev

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/dev/leader"
	"github.com/ocelhq/ocel/cli/internal/devresources"
	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/exitcode"

	"github.com/ocelhq/ocel/cli/internal/clitest"
)

func TestRunRun(t *testing.T) {
	t.Run("with no leader it runs on its own, resolves, runs and tears down", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		deps := devDeps()

		root := t.TempDir()
		t.Cleanup(func() { releaseLeader(root) })

		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		envDumpPath := filepath.Join(root, "env.out")
		appCmd := []string{"sh", "-c", dumpEnvAndLiveDir(envDumpPath) + "; exit 7"}

		var stdout, stderr bytes.Buffer
		err := runRun(context.Background(), deps, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		t.Run("the child's exit code becomes the command's", func(t *testing.T) {
			var exitErr *exitcode.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("runRun err = %v, want *exitcode.ExitError; stderr=%s", err, stderr.String())
			}
			if exitErr.Code != 7 {
				t.Fatalf("ExitError.Code = %d, want 7", exitErr.Code)
			}
		})

		t.Run("the resolved resource reaches the child as a file under its live dir, and not through its environment", func(t *testing.T) {
			if _, ok := readDump(t, envDumpPath)["OCEL_RESOURCE_POSTGRES_main"]; ok {
				t.Errorf("the child's environment holds OCEL_RESOURCE_POSTGRES_main")
			}
			raw, ok := readDump(t, envDumpPath+".live")["OCEL_RESOURCE_POSTGRES_main"]
			if !ok {
				t.Fatalf("the live dir holds no OCEL_RESOURCE_POSTGRES_main")
			}
			if !strings.Contains(raw, `"postgres"`) {
				t.Fatalf("OCEL_RESOURCE_POSTGRES_main = %q, want it to contain a postgres link", raw)
			}
		})

		t.Run("it leaves no leader record behind, having never advertised itself as leader", func(t *testing.T) {
			if _, err := leader.Read(root); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("leader.Read err = %v, want a not-exist error (ocel run must not advertise as leader)", err)
			}
		})
	})

	t.Run("with a running leader it reuses the leader's env and runs once", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		deps := devDeps()

		root := t.TempDir()
		t.Cleanup(func() { releaseLeader(root) })

		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()

		leaderDone := make(chan error, 1)
		var leaderStdout, leaderStderr syncBuffer
		go func() {
			leaderDone <- runDev(leaderCtx, deps, false, root, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
		}()

		waitForLeaderRecord(t, root)

		envDumpPath := filepath.Join(root, "run-env.out")
		runAppArgs := []string{"sh", "-c", dumpEnvAndLiveDir(envDumpPath) + "; exit 9"}

		var stdout, stderr bytes.Buffer
		err := runRun(context.Background(), deps, root, runAppArgs, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitcode.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("runRun err = %v, want *exitcode.ExitError; stderr=%s", err, stderr.String())
		}
		if exitErr.Code != 9 {
			t.Fatalf("ExitError.Code = %d, want 9", exitErr.Code)
		}

		if _, ok := readDump(t, envDumpPath)["OCEL_RESOURCE_POSTGRES_main"]; ok {
			t.Errorf("the run's environment holds OCEL_RESOURCE_POSTGRES_main")
		}
		raw, ok := readDump(t, envDumpPath+".live")["OCEL_RESOURCE_POSTGRES_main"]
		if !ok {
			t.Fatalf("the run's live dir holds no OCEL_RESOURCE_POSTGRES_main")
		}
		if !strings.Contains(raw, `"postgres"`) {
			t.Fatalf("OCEL_RESOURCE_POSTGRES_main = %q, want it to contain a postgres link", raw)
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

		root := t.TempDir()
		t.Cleanup(func() { releaseLeader(root) })

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		server := devserver.New("http://"+listener.Addr().String(), devresources.New("a-leader", devresources.Options{}))
		server.PushEnv(map[string]string{"OCEL_RESOURCE_POSTGRES_main": `{"name":"main","postgres":{"host":"resolved","port":5432,"database":"main","username":"u","password":"p"}}`})

		httpServer := &http.Server{Handler: server.Mux()}
		go httpServer.Serve(listener)
		defer httpServer.Close()

		if err := leader.Claim(root, leader.Leader{Address: listener.Addr().String(), Token: server.AppToken()}); err != nil {
			t.Fatalf("leader.Claim: %v", err)
		}

		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)

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
