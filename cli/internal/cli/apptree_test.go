package cli

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/lockfile"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func fixtureWorkerTree(t *testing.T, root, name string) (appArgs []string, startedPath, pidPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}
	startedPath = filepath.Join(root, name+".started")
	pidPath = filepath.Join(root, name+".workerpid")
	appArgs = []string{"sh", "-c", "sleep 30 & echo $! > " + pidPath + "; touch " + startedPath + "; wait"}
	return appArgs, startedPath, pidPath
}

func fixtureDeepWorkerTree(t *testing.T, root, name string) (appArgs []string, startedPath, leafPidPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}
	scriptPath := filepath.Join(root, name+".sh")
	startedPath = filepath.Join(root, name+".started")
	pidPrefix := filepath.Join(root, name+".workerpid.")
	clitest.WriteFile(t, scriptPath, `#!/bin/sh
depth="$1"
started="$2"
pidprefix="$3"
trap '' INT
echo $$ > "${pidprefix}${depth}"
if [ "$depth" -ge 3 ]; then
  touch "$started"
  exec sleep 30
fi
next=$((depth + 1))
sh "$0" "$next" "$started" "$pidprefix" &
child=$!
wait "$child"
`)
	appArgs = []string{"sh", scriptPath, "1", startedPath, pidPrefix}
	leafPidPath = pidPrefix + "3"
	return appArgs, startedPath, leafPidPath
}

func waitProcessDead(t *testing.T, pidPath string) {
	t.Helper()
	waitForFile(t, pidPath)
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse worker pid %q: %v", raw, err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("worker pid %d is still alive after the CLI exited", pid)
}

func TestProcessTreeDiesWithTheCLI(t *testing.T) {
	t.Run("a standalone `ocel run` kills its worker's grandchildren", func(t *testing.T) {
		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		deps := newDeps()
		withCredentials(&deps, resolveServer.URL)

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		writeLink(t, root, resolveServer.URL, testProjectID(t))
		clitest.WriteFile(t, filepath.Join(root, "ocel", "main.ts"), declareResourceScript("main"))

		appArgs, startedPath, pidPath := fixtureWorkerTree(t, root, "run")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var stdout, stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- runRun(ctx, deps, nil, root, appArgs, &stdout, &stderr, strings.NewReader(""))
		}()

		waitForFile(t, startedPath)
		cancel()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("runRun did not exit after cancellation")
		}

		waitProcessDead(t, pidPath)
	})

	t.Run("a standalone `ocel run` kills a 3-level deep descendant, non-tty", func(t *testing.T) {
		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		deps := newDeps()
		withCredentials(&deps, resolveServer.URL)

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		writeLink(t, root, resolveServer.URL, testProjectID(t))
		clitest.WriteFile(t, filepath.Join(root, "ocel", "main.ts"), declareResourceScript("main"))

		appArgs, startedPath, leafPidPath := fixtureDeepWorkerTree(t, root, "run-deep")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var stdout, stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- runRun(ctx, deps, nil, root, appArgs, &stdout, &stderr, strings.NewReader(""))
		}()

		waitForFile(t, startedPath)
		cancel()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("runRun did not exit after cancellation")
		}

		waitProcessDead(t, leafPidPath)
	})

	t.Run("a leader `ocel dev` kills its app's grandchildren", func(t *testing.T) {
		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		deps := newDeps()
		withCredentials(&deps, resolveServer.URL)

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		writeLink(t, root, resolveServer.URL, testProjectID(t))
		clitest.WriteFile(t, filepath.Join(root, "ocel", "main.ts"), declareResourceScript("main"))

		appArgs, startedPath, pidPath := fixtureWorkerTree(t, root, "leader")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var stdout, stderr syncBuffer
		done := make(chan error, 1)
		go func() {
			done <- runDev(ctx, deps, nil, root, appArgs, &stdout, &stderr, strings.NewReader(""))
		}()

		waitForLockfile(t, root)
		waitForFile(t, startedPath)
		cancel()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("runDev (leader) did not exit after cancellation")
		}

		waitProcessDead(t, pidPath)
	})

	t.Run("a follower `ocel dev` kills its app's grandchildren", func(t *testing.T) {
		deps := newDeps()
		clitest.SetLoggedIn(&deps)

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		projectID := testProjectID(t)
		const apiURL = "https://api.example.com"
		srv := devserver.New(apiURL, "tok", projectID, "http://127.0.0.1:0")
		srv.PushEnv(map[string]string{"OCEL_RESOURCE_POSTGRES_main": `{"name":"main","postgres":{"host":"resolved","port":5432,"database":"main","username":"u","password":"p"}}`})

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		httpSrv := &http.Server{Handler: srv.Mux()}
		go httpSrv.Serve(listener)
		defer httpSrv.Close()

		if err := lockfile.Create(root, listener.Addr().String()); err != nil {
			t.Fatalf("lockfile.Create: %v", err)
		}

		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		writeLink(t, root, apiURL, projectID)

		appArgs, startedPath, pidPath := fixtureWorkerTree(t, root, "follower")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var stdout, stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- runDev(ctx, deps, nil, root, appArgs, &stdout, &stderr, strings.NewReader(""))
		}()

		waitForFile(t, startedPath)
		cancel()

		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				var exitErr *exitsig.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("runDev (follower) err = %v, want nil or *exitsig.ExitError", err)
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatal("runDev (follower) did not exit after cancellation")
		}

		waitProcessDead(t, pidPath)
	})
}
