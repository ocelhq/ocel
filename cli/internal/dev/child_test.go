package dev

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/childprocess/childprocesstest"

	"github.com/ocelhq/ocel/cli/internal/dev/leader"
	"github.com/ocelhq/ocel/cli/internal/devresources"
	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/exitcode"

	"github.com/ocelhq/ocel/cli/internal/clitest"
)

func TestProcessTreeDiesWithTheCLI(t *testing.T) {
	t.Run("a standalone `ocel run` kills its worker's grandchildren", func(t *testing.T) {

		deps := devDeps()

		root := t.TempDir()
		t.Cleanup(func() { releaseLeader(root) })

		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		appArgs, startedPath, pidPath := childprocesstest.WorkerTree(t, root, "run")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var stdout, stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- runRun(ctx, deps, root, appArgs, &stdout, &stderr, strings.NewReader(""))
		}()

		childprocesstest.WaitForFile(t, startedPath)
		cancel()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("runRun did not exit after cancellation")
		}

		childprocesstest.WaitDead(t, pidPath)
	})

	t.Run("a standalone `ocel run` kills a 3-level deep descendant, non-tty", func(t *testing.T) {

		deps := devDeps()

		root := t.TempDir()
		t.Cleanup(func() { releaseLeader(root) })

		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		appArgs, startedPath, leafPidPath := childprocesstest.DeepWorkerTree(t, root, "run-deep")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var stdout, stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- runRun(ctx, deps, root, appArgs, &stdout, &stderr, strings.NewReader(""))
		}()

		childprocesstest.WaitForFile(t, startedPath)
		cancel()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("runRun did not exit after cancellation")
		}

		childprocesstest.WaitDead(t, leafPidPath)
	})

	t.Run("a leader `ocel dev` kills its app's grandchildren", func(t *testing.T) {

		deps := devDeps()

		root := t.TempDir()
		t.Cleanup(func() { releaseLeader(root) })

		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		appArgs, startedPath, pidPath := childprocesstest.WorkerTree(t, root, "leader")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var stdout, stderr syncBuffer
		done := make(chan error, 1)
		go func() {
			done <- runDev(ctx, deps, false, root, appArgs, &stdout, &stderr, strings.NewReader(""))
		}()

		waitForLeaderRecord(t, root)
		childprocesstest.WaitForFile(t, startedPath)
		cancel()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("runDev (leader) did not exit after cancellation")
		}

		childprocesstest.WaitDead(t, pidPath)
	})

	t.Run("a follower `ocel dev` kills its app's grandchildren", func(t *testing.T) {
		deps := devDeps()

		root := t.TempDir()
		t.Cleanup(func() { releaseLeader(root) })

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		srv := devserver.New("http://"+listener.Addr().String(), devresources.New("a-leader", devresources.Options{}))
		srv.PushEnv(map[string]string{"OCEL_RESOURCE_POSTGRES_main": `{"name":"main","postgres":{"host":"resolved","port":5432,"database":"main","username":"u","password":"p"}}`})

		httpSrv := &http.Server{Handler: srv.Mux()}
		go httpSrv.Serve(listener)
		defer httpSrv.Close()

		if err := leader.Claim(root, leader.Leader{Address: listener.Addr().String(), Token: srv.AppToken()}); err != nil {
			t.Fatalf("leader.Claim: %v", err)
		}

		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)

		appArgs, startedPath, pidPath := childprocesstest.WorkerTree(t, root, "follower")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var stdout, stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- runDev(ctx, deps, false, root, appArgs, &stdout, &stderr, strings.NewReader(""))
		}()

		childprocesstest.WaitForFile(t, startedPath)
		cancel()

		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				var exitErr *exitcode.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("runDev (follower) err = %v, want nil or *exitcode.ExitError", err)
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatal("runDev (follower) did not exit after cancellation")
		}

		childprocesstest.WaitDead(t, pidPath)
	})
}

func TestExitErrorReportsInterruptWhenCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, err := range []error{nil, context.Canceled, &exitcode.ExitError{Code: 255}} {
		got := exitError(ctx, err)
		var exitErr *exitcode.ExitError
		if !errors.As(got, &exitErr) || exitErr.Code != exitcode.Interrupt {
			t.Errorf("exitError(cancelled, %v) = %v, want *exitcode.ExitError with code %d", err, got, exitcode.Interrupt)
		}
	}
}

func TestExitErrorKeepsTheChildsCodeWhenNotCancelled(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("sh", "-c", "exit 3")
	waitErr := cmd.Run()

	got := exitError(context.Background(), waitErr)
	var exitErr *exitcode.ExitError
	if !errors.As(got, &exitErr) || exitErr.Code != 3 {
		t.Fatalf("exitError = %v, want *exitcode.ExitError with code 3", got)
	}
	if got := exitError(context.Background(), nil); got != nil {
		t.Errorf("exitError(nil) = %v, want nil", got)
	}
}
