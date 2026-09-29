package dev

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/childprocess/childprocesstest"

	"github.com/ocelhq/ocel/cli/internal/dev/leader"
	"github.com/ocelhq/ocel/cli/internal/devresources"
	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/dotfile"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/cli/internal/filewatch"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/processenv"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/statedir"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker/dockertest"
)

func TestRunDev(t *testing.T) {
	t.Run("with no config file it discovers, declares, syncs and spawns", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		deps := devDeps()

		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		envDumpPath := filepath.Join(root, "env.out")
		appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), deps, false, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		t.Run("the child's exit code becomes the command's", func(t *testing.T) {
			var exitErr *exitcode.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("runDev err = %v, want *exitcode.ExitError; stderr=%s", err, stderr.String())
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
				t.Fatalf("OCEL_RESOURCE_POSTGRES_main = %q, want it to contain a postgres link", raw)
			}
		})
	})

	t.Run("it joins the watcher before it returns", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		deps := devDeps()

		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), deps, false, root, []string{"sh", "-c", "exit 7"}, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitcode.ExitError
		if !errors.As(err, &exitErr) || exitErr.Code != 7 {
			t.Fatalf("runDev err = %v, want exit 7; stderr=%s", err, stderr.String())
		}

		for _, frame := range []string{
			"github.com/ocelhq/ocel/cli/internal/filewatch.run",
		} {
			if stacks := goroutineStacks(t); strings.Contains(stacks, frame) {
				t.Errorf("%s still running after runDev returned; it can still write into the project directory:\n%s", frame, stacks)
			}
		}
	})

	t.Run("a second run in the same root from a subdirectory becomes a follower and receives the pushed env", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		deps := devDeps()

		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app", apps: [{ name: "web", path: "apps/web", folder: "/web" }] };
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

		envDumpPath := filepath.Join(root, "follower-env.out")
		followerAppArgs := []string{"sh", "-c", "env > " + envDumpPath + "; exit 9"}

		subdir := filepath.Join(root, "apps", "web")
		clitest.WriteFile(t, filepath.Join(subdir, "package.json"), `{"name":"web"}`)
		clitest.WriteFile(t, filepath.Join(subdir, "index.ts"), "export {};\n")

		var followerStdout, followerStderr bytes.Buffer
		err := runDev(context.Background(), deps, false, subdir, followerAppArgs, &followerStdout, &followerStderr, strings.NewReader(""))

		var exitErr *exitcode.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("follower runDev err = %v, want *exitcode.ExitError; stderr=%s", err, followerStderr.String())
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
		if !strings.Contains(raw, `"postgres"`) {
			t.Fatalf("OCEL_RESOURCE_POSTGRES_main = %q, want it to contain a postgres link", raw)
		}

		if got, ok := env[processenv.AppFolderEnvVar]; !ok || got != "/web" {
			t.Errorf("follower %s = %q (present=%v), want the folder the app binds", processenv.AppFolderEnvVar, got, ok)
		}

		cancelLeader()
		select {
		case <-leaderDone:
		case <-time.After(5 * time.Second):
			t.Fatal("leader runDev did not exit after cancellation")
		}
	})

	t.Run("a second clone of the project elects its own leader", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		deps := devDeps()

		firstClone := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(firstClone) })
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(firstClone), "main.ts"), declareResourceScript("first"))

		secondClone := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(secondClone) })
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(secondClone), "main.ts"), declareResourceScript("second"))

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()

		leaderDone := make(chan error, 1)
		var leaderStdout, leaderStderr syncBuffer
		go func() {
			leaderDone <- runDev(leaderCtx, deps, false, firstClone, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
		}()

		waitForLeaderRecord(t, firstClone)

		envDumpPath := filepath.Join(secondClone, "env.out")
		appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 9"}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), deps, false, secondClone, appCmd, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitcode.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("second clone runDev err = %v, want *exitcode.ExitError; stderr=%s", err, stderr.String())
		}
		if exitErr.Code != 9 {
			t.Fatalf("second clone ExitError.Code = %d, want 9", exitErr.Code)
		}

		dumped, err := os.ReadFile(envDumpPath)
		if err != nil {
			t.Fatalf("read env dump: %v", err)
		}
		env := toMap(strings.Split(strings.TrimRight(string(dumped), "\n"), "\n"))

		if _, ok := env["OCEL_RESOURCE_POSTGRES_second"]; !ok {
			t.Fatalf("second clone env missing its own OCEL_RESOURCE_POSTGRES_second, got: %s", dumped)
		}
		if _, ok := env["OCEL_RESOURCE_POSTGRES_first"]; ok {
			t.Fatalf("second clone inherited the other clone's resolved env, got: %s", dumped)
		}

		cancelLeader()
		select {
		case <-leaderDone:
		case <-time.After(5 * time.Second):
			t.Fatal("leader runDev did not exit after cancellation")
		}
	})

	t.Run("a file change re-resolves and pushes the updated env to the follower", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		deps := devDeps()

		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()

		var leaderStdout, leaderStderr syncBuffer

		leaderDone := make(chan error, 1)
		go func() {
			leaderDone <- runDev(leaderCtx, deps, false, root, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
		}()

		waitForLeaderRecord(t, root)

		envDumpPath := filepath.Join(root, "follower-env.out")
		followerAppArgs := []string{"sh", "-c", "while true; do env > " + envDumpPath + "; sleep 0.02; done"}

		followerCtx, cancelFollower := context.WithCancel(context.Background())
		defer cancelFollower()
		followerDone := make(chan error, 1)
		var followerStdout, followerStderr bytes.Buffer
		go func() {
			followerDone <- runDev(followerCtx, deps, false, root, followerAppArgs, &followerStdout, &followerStderr, strings.NewReader(""))
		}()

		waitForEnvVar(t, envDumpPath, "OCEL_RESOURCE_POSTGRES_main")

		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "second.ts"), declareResourceScript("second"))

		waitForEnvVar(t, envDumpPath, "OCEL_RESOURCE_POSTGRES_second")

		cancelFollower()
		select {
		case <-followerDone:
		case <-time.After(5 * time.Second):
			t.Fatal("follower runDev did not exit after cancellation")
		}

		cancelLeader()
		select {
		case <-leaderDone:
		case <-time.After(5 * time.Second):
			t.Fatal("leader runDev did not exit after cancellation")
		}
	})

	t.Run("editing the dotfile re-resolves and pushes the new value", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"API_TOKEN","class":"VARIABLE_CLASS_PLAIN","required":true}`))
		clitest.WriteFile(t, filepath.Join(root, dotfile.FileName), "API_TOKEN=first\n")

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()

		var leaderStdout, leaderStderr syncBuffer
		leaderDone := make(chan error, 1)
		go func() {
			leaderDone <- runDev(leaderCtx, deps, false, root, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
		}()

		waitForLeaderRecord(t, root)

		envDumpPath := filepath.Join(root, "follower-env.out")
		followerAppArgs := []string{"sh", "-c", "while true; do env > " + envDumpPath + "; sleep 0.02; done"}

		followerCtx, cancelFollower := context.WithCancel(context.Background())
		defer cancelFollower()
		followerDone := make(chan error, 1)
		var followerStdout, followerStderr bytes.Buffer
		go func() {
			followerDone <- runDev(followerCtx, deps, false, root, followerAppArgs, &followerStdout, &followerStderr, strings.NewReader(""))
		}()

		waitForEnvValue(t, envDumpPath, "API_TOKEN", "first")

		clitest.WriteFile(t, filepath.Join(root, dotfile.FileName), "API_TOKEN=second\n")

		waitForEnvValue(t, envDumpPath, "API_TOKEN", "second")

		cancelFollower()
		select {
		case <-followerDone:
		case <-time.After(5 * time.Second):
			t.Fatal("follower runDev did not exit after cancellation")
		}

		cancelLeader()
		select {
		case <-leaderDone:
		case <-time.After(5 * time.Second):
			t.Fatal("leader runDev did not exit after cancellation")
		}
	})

	t.Run("editing the dotfile restarts the leader's own app with the new value", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"API_TOKEN","class":"VARIABLE_CLASS_PLAIN","required":true}`))
		clitest.WriteFile(t, filepath.Join(root, dotfile.FileName), "API_TOKEN=first\n")

		envDumpPath := filepath.Join(root, "leader-env.out")
		leaderAppArgs := []string{"sh", "-c", "while true; do env > " + envDumpPath + "; sleep 0.02; done"}

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()

		var leaderStdout, leaderStderr syncBuffer
		leaderDone := make(chan error, 1)
		go func() {
			leaderDone <- runDev(leaderCtx, deps, false, root, leaderAppArgs, &leaderStdout, &leaderStderr, strings.NewReader(""))
		}()

		waitForEnvValue(t, envDumpPath, "API_TOKEN", "first")

		clitest.WriteFile(t, filepath.Join(root, dotfile.FileName), "API_TOKEN=second\n")

		waitForEnvValue(t, envDumpPath, "API_TOKEN", "second")

		cancelLeader()
		select {
		case <-leaderDone:
		case <-time.After(5 * time.Second):
			t.Fatal("leader runDev did not exit after cancellation")
		}
	})

	t.Run("a refusal the edit fixes stops refusing", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"API_TOKEN","class":"VARIABLE_CLASS_PLAIN","required":true}`))
		clitest.WriteFile(t, filepath.Join(root, dotfile.FileName), "API_TOKEN=first\n")

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()

		var leaderStdout, leaderStderr syncBuffer
		leaderDone := make(chan error, 1)
		go func() {
			leaderDone <- runDev(leaderCtx, deps, false, root, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
		}()

		waitForLeaderRecord(t, root)

		envDumpPath := filepath.Join(root, "follower-env.out")
		followerAppArgs := []string{"sh", "-c", "while true; do env > " + envDumpPath + "; sleep 0.02; done"}

		followerCtx, cancelFollower := context.WithCancel(context.Background())
		defer cancelFollower()
		followerDone := make(chan error, 1)
		var followerStdout, followerStderr bytes.Buffer
		go func() {
			followerDone <- runDev(followerCtx, deps, false, root, followerAppArgs, &followerStdout, &followerStderr, strings.NewReader(""))
		}()

		waitForEnvValue(t, envDumpPath, "API_TOKEN", "first")

		clitest.WriteFile(t, filepath.Join(root, dotfile.FileName), "# the value the run needs, deleted\n")
		waitForOutput(t, &leaderStderr, "API_TOKEN")
		if got := leaderStderr.String(); !strings.Contains(got, dotfile.FileName) {
			t.Errorf("stderr = %q, want the mid-session refusal to name %s", got, dotfile.FileName)
		}

		clitest.WriteFile(t, filepath.Join(root, dotfile.FileName), "API_TOKEN=restored\n")
		waitForEnvValue(t, envDumpPath, "API_TOKEN", "restored")

		cancelFollower()
		select {
		case <-followerDone:
		case <-time.After(5 * time.Second):
			t.Fatal("follower runDev did not exit after cancellation")
		}

		cancelLeader()
		select {
		case <-leaderDone:
		case <-time.After(5 * time.Second):
			t.Fatal("leader runDev did not exit after cancellation")
		}
	})

	t.Run("an edit made the instant a follower sees the first push is still watched", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		stalled := startWatching
		startWatching = func(ctx context.Context, srv *devserver.Server, cfg *project.Project, invoked invocation, session *run.Span, onResolved func(map[string]string)) (*filewatch.Watcher, error) {
			time.Sleep(300 * time.Millisecond)
			return stalled(ctx, srv, cfg, invoked, session, onResolved)
		}
		t.Cleanup(func() { startWatching = stalled })

		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"API_TOKEN","class":"VARIABLE_CLASS_PLAIN","required":true}`))
		clitest.WriteFile(t, filepath.Join(root, dotfile.FileName), "API_TOKEN=first\n")

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()

		var leaderStdout, leaderStderr syncBuffer
		leaderDone := make(chan error, 1)
		go func() {
			leaderDone <- runDev(leaderCtx, deps, false, root, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
		}()

		waitForLeaderRecord(t, root)

		envDumpPath := filepath.Join(root, "follower-env.out")
		followerAppArgs := []string{"sh", "-c", "while true; do env > " + envDumpPath + "; sleep 0.02; done"}

		followerCtx, cancelFollower := context.WithCancel(context.Background())
		defer cancelFollower()
		followerDone := make(chan error, 1)
		var followerStdout, followerStderr bytes.Buffer
		go func() {
			followerDone <- runDev(followerCtx, deps, false, root, followerAppArgs, &followerStdout, &followerStderr, strings.NewReader(""))
		}()

		waitForEnvValue(t, envDumpPath, "API_TOKEN", "first")

		clitest.WriteFile(t, filepath.Join(root, dotfile.FileName), "API_TOKEN=second\n")
		waitForEnvValue(t, envDumpPath, "API_TOKEN", "second")

		cancelFollower()
		select {
		case <-followerDone:
		case <-time.After(5 * time.Second):
			t.Fatal("follower runDev did not exit after cancellation")
		}

		cancelLeader()
		select {
		case <-leaderDone:
		case <-time.After(5 * time.Second):
			t.Fatal("leader runDev did not exit after cancellation")
		}
	})

	t.Run("an edit that introduces an unreadable line says so", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"API_TOKEN","class":"VARIABLE_CLASS_PLAIN","required":true}`))
		clitest.WriteFile(t, filepath.Join(root, dotfile.FileName), "API_TOKEN=first\n")

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()

		var leaderStdout, leaderStderr syncBuffer
		leaderDone := make(chan error, 1)
		go func() {
			leaderDone <- runDev(leaderCtx, deps, false, root, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
		}()

		waitForLeaderRecord(t, root)

		waitForOutputAfter(t, &leaderStderr, "line 2", func() {
			clitest.WriteFile(t, filepath.Join(root, dotfile.FileName), "API_TOKEN=first\nnot a pair\n")
		})

		cancelLeader()
		select {
		case <-leaderDone:
		case <-time.After(5 * time.Second):
			t.Fatal("leader runDev did not exit after cancellation")
		}
	})

	t.Run("a follower whose leader disconnects stops its child, says so and exits non-zero", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		deps := devDeps()

		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		srv := devserver.New("http://"+listener.Addr().String(), devresources.New("a-leader", devresources.Options{}))
		srv.PushEnv(map[string]string{"OCEL_RESOURCE_POSTGRES_main": `{"name":"main","postgres":{"host":"resolved","port":5432,"database":"main","username":"u","password":"p"}}`})

		httpSrv := &http.Server{Handler: srv.Mux()}
		go httpSrv.Serve(listener)

		if err := leader.Claim(root, leader.Leader{Address: listener.Addr().String(), Token: srv.AppToken()}); err != nil {
			t.Fatalf("leader.Claim: %v", err)
		}

		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)

		startedPath := filepath.Join(root, "started")
		appArgs := []string{"sh", "-c", "touch " + startedPath + "; sleep 10"}

		followerDone := make(chan error, 1)
		var stdout, stderr bytes.Buffer
		go func() {
			followerDone <- runDev(context.Background(), deps, false, root, appArgs, &stdout, &stderr, strings.NewReader(""))
		}()

		childprocesstest.WaitForFile(t, startedPath)

		if err := httpSrv.Close(); err != nil {
			t.Fatalf("close fake leader: %v", err)
		}

		select {
		case err := <-followerDone:
			if err == nil {
				t.Fatalf("follower runDev err = nil, want the disconnect to fail the run; stderr=%s", stderr.String())
			}
			if !strings.Contains(stderr.String(), "restart `ocel dev`") {
				t.Fatalf("stderr = %q, want it to mention restarting the leader", stderr.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("follower runDev did not exit after leader disconnect")
		}
	})
}

func TestDevSuppliesDeclaredResourcesItself(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	t.Run("a declared postgres comes from the dev resources, says where it landed, and stops with the run", func(t *testing.T) {
		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		engine := &dockertest.Engine{}
		deps := devDeps()
		deps.OpenDocker = engine.OpenFunc()

		envDumpPath := filepath.Join(root, "env.out")
		var stdout, stderr syncBuffer
		err := runDev(context.Background(), deps, false, root, []string{"sh", "-c", "env > " + envDumpPath}, &stdout, &stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("runDev err = %v; stderr=%s", err, stderr.String())
		}

		dumped, readErr := os.ReadFile(envDumpPath)
		if readErr != nil {
			t.Fatalf("read env dump: %v", readErr)
		}
		env := toMap(strings.Split(strings.TrimRight(string(dumped), "\n"), "\n"))
		if raw := env["OCEL_RESOURCE_POSTGRES_main"]; !strings.Contains(raw, `"host":"127.0.0.1"`) || !strings.Contains(raw, `"database":"main"`) {
			t.Errorf("OCEL_RESOURCE_POSTGRES_main = %q, want the binding of the container the run started", raw)
		}
		if !strings.Contains(stderr.String(), `postgres "main" → postgres:17 @ 127.0.0.1:`) {
			t.Errorf("stderr = %q, want one line saying where postgres \"main\" landed", stderr.String())
		}
		if len(engine.Specs) != 1 || engine.Specs[0].Labels["dev.ocel.project"] == "" {
			t.Fatalf("ran %+v, want one container labelled with the project", engine.Specs)
		}
		if len(engine.Stopped) != 1 {
			t.Errorf("stopped %v, want the run's container stopped on exit", engine.Stopped)
		}
		if len(engine.Wiped) != 0 {
			t.Errorf("wiped %v, want volumes kept when --reset was not asked for", engine.Wiped)
		}
	})

	t.Run("an app that declares no resource never needs docker", func(t *testing.T) {
		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		deps := devDeps()
		deps.OpenDocker = func(context.Context) (docker.Engine, error) {
			t.Error("docker was opened for an app that declares nothing")
			return nil, errors.New("no daemon")
		}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), deps, false, root, []string{"sh", "-c", "exit 0"}, &stdout, &stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("runDev err = %v; stderr=%s", err, stderr.String())
		}
	})

	t.Run("with no docker daemon the refusal names the resource that needed one", func(t *testing.T) {
		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		deps := devDeps()
		deps.OpenDocker = func(context.Context) (docker.Engine, error) {
			return nil, &docker.Unreachable{Address: "unix:///var/run/docker.sock", Err: errors.New("connect: no such file or directory")}
		}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), deps, false, root, []string{"sh", "-c", "exit 0"}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDev started the app without the postgres it declared")
		}
		for _, want := range []string{`postgres "main"`, "start docker", "DOCKER_HOST"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %q, want it to mention %q", err.Error(), want)
			}
		}
	})

	t.Run("--reset wipes this project's volumes before anything starts", func(t *testing.T) {
		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })

		engine := &dockertest.Engine{}
		deps := devDeps()
		deps.OpenDocker = engine.OpenFunc()

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), deps, true, root, []string{"sh", "-c", "exit 0"}, &stdout, &stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("runDev err = %v; stderr=%s", err, stderr.String())
		}
		if len(engine.Wiped) != 1 || engine.Wiped[0]["dev.ocel.project"] == "" || len(engine.Wiped[0]) != 1 {
			t.Fatalf("wiped %v, want exactly this project's volumes", engine.Wiped)
		}
	})

	t.Run("`ocel run` on its own gets the same resources", func(t *testing.T) {
		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		engine := &dockertest.Engine{}
		deps := devDeps()
		deps.OpenDocker = engine.OpenFunc()

		envDumpPath := filepath.Join(root, "env.out")
		var stdout, stderr syncBuffer
		if err := runRun(context.Background(), deps, root, []string{"sh", "-c", "env > " + envDumpPath}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runRun err = %v; stderr=%s", err, stderr.String())
		}
		dumped, readErr := os.ReadFile(envDumpPath)
		if readErr != nil {
			t.Fatalf("read env dump: %v", readErr)
		}
		if !strings.Contains(string(dumped), "OCEL_RESOURCE_POSTGRES_main=") {
			t.Errorf("command env has no OCEL_RESOURCE_POSTGRES_main: %s", dumped)
		}
		if len(engine.Stopped) != 1 {
			t.Errorf("stopped %v, want the command's container stopped once it exited", engine.Stopped)
		}
	})
}

type stopWatchingEngine struct {
	*dockertest.Engine
	onStop func()
}

func (e stopWatchingEngine) Stop(ctx context.Context, id string) error {
	e.onStop()
	return e.Engine.Stop(ctx, id)
}

func TestDevLeavesNothingBehind(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	t.Run("an interrupt while a resource is still coming up stops the container it started", func(t *testing.T) {
		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		ctx, interrupt := context.WithCancel(context.Background())
		engine := &dockertest.Engine{}
		engine.Answer = func(argv []string) (string, error) {
			if argv[0] == "pg_isready" {
				interrupt()
				return "", errors.New("no response")
			}
			return "", nil
		}
		deps := devDeps()
		deps.OpenDocker = engine.OpenFunc()

		var stdout, stderr syncBuffer
		if err := runDev(ctx, deps, false, root, []string{"sh", "-c", "exit 0"}, &stdout, &stderr, strings.NewReader("")); err == nil {
			t.Fatal("runDev = nil for a startup that was interrupted")
		}
		if len(engine.Specs) != 1 || len(engine.Stopped) != 1 {
			t.Fatalf("ran %d containers and stopped %v, want the one that was started stopped; stderr=%s", len(engine.Specs), engine.Stopped, stderr.String())
		}
	})

	t.Run("the leader keeps its record until its containers are stopped", func(t *testing.T) {
		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		var recordAtStop error
		engine := stopWatchingEngine{Engine: &dockertest.Engine{}, onStop: func() { _, recordAtStop = leader.Read(root) }}
		deps := devDeps()
		deps.OpenDocker = func(context.Context) (docker.Engine, error) { return engine, nil }

		var stdout, stderr syncBuffer
		if err := runDev(context.Background(), deps, false, root, []string{"sh", "-c", "exit 0"}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDev err = %v; stderr=%s", err, stderr.String())
		}
		if len(engine.Stopped) != 1 {
			t.Fatalf("stopped %v, want the run's container stopped", engine.Stopped)
		}
		if recordAtStop != nil {
			t.Fatalf("the leader record was already gone while the container was being stopped: %v", recordAtStop)
		}
		if _, err := leader.Read(root); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("leader.Read after exit = %v, want the leader record released", err)
		}
	})

}

func goroutineStacks(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 1); err != nil {
		t.Fatalf("goroutine profile: %v", err)
	}
	return buf.String()
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

type testDeps struct {
	OpenDocker docker.OpenFunc
	LogFormat  terminal.Format
}

func devDeps() testDeps {
	return testDeps{OpenDocker: (&dockertest.Engine{}).OpenFunc(), LogFormat: terminal.FormatHuman}
}

func options(ctx context.Context, deps testDeps, cwd string, command []string, stdout, stderr io.Writer, stdin io.Reader) (Options, error) {
	cfg, err := project.LoadOptional(ctx, cwd, "")
	if err != nil {
		return Options{}, err
	}
	return Options{Project: cfg, Command: command, OpenDocker: deps.OpenDocker, Stdin: stdin, Stdout: stdout, Stderr: stderr}, nil
}

func runDev(ctx context.Context, deps testDeps, reset bool, cwd string, command []string, stdout, stderr io.Writer, stdin io.Reader) error {
	opts, err := options(ctx, deps, cwd, command, stdout, stderr, stdin)
	if err != nil {
		return err
	}
	return underRun(ctx, deps, "ocel dev", opts, func(ctx context.Context, opts Options) error {
		return Run(ctx, opts, reset)
	})
}

func runRun(ctx context.Context, deps testDeps, cwd string, command []string, stdout, stderr io.Writer, stdin io.Reader) error {
	opts, err := options(ctx, deps, cwd, command, stdout, stderr, stdin)
	if err != nil {
		return err
	}
	return underRun(ctx, deps, "ocel run", opts, func(ctx context.Context, opts Options) error {
		return RunOnce(ctx, opts, cwd)
	})
}

func underRun(ctx context.Context, deps testDeps, command string, opts Options, body func(context.Context, Options) error) error {
	output := &lockedWriter{w: opts.Stderr}
	opts.Stderr = output
	bus := run.NewBus(time.Now)
	bus.Attach(terminal.NewSink(terminal.Resolve(terminal.Conditions{LogFormat: deps.LogFormat}), output))
	defer bus.Close()
	ctx, begun, err := bus.Begin(ctx, command, opts.Project.Dir)
	if err != nil {
		return err
	}
	opts.Run = begun
	err = body(ctx, opts)
	ended := err
	begun.End(&ended)
	return err
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func waitForLeaderRecord(t *testing.T, root string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := leader.Read(root); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the leader record for %q never appeared", root)
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func declareResourceScript(name string) string {
	return fmt.Sprintf(`
declare global {
  var __ocelRegister: Promise<unknown>[];
}
globalThis.__ocelRegister ??= [];
globalThis.__ocelRegister.push(
  fetch(new URL("/app.resources.v1.ResourceService/Declare", process.env.%s), {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + process.env.`+processenv.DevServerTokenEnvVar+` },
    body: JSON.stringify({
      resource: { type: "RESOURCE_TYPE_POSTGRES", name: %q },
      postgres: { version: "17" },
    }),
  }),
);
export {};
`, processenv.DevServerEnvVar, name)
}

func waitForEnvVar(t *testing.T, path, key string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if dumped, err := os.ReadFile(path); err == nil {
			env := toMap(strings.Split(strings.TrimRight(string(dumped), "\n"), "\n"))
			if _, ok := env[key]; ok {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%q never contained env key %q", path, key)
}

func waitForEnvValue(t *testing.T, path, key, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		if dumped, err := os.ReadFile(path); err == nil {
			env := toMap(strings.Split(strings.TrimRight(string(dumped), "\n"), "\n"))
			if env[key] == want {
				return
			}
			last = env[key]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s in %q = %q, never became %q", key, path, last, want)
}

func waitForOutputAfter(t *testing.T, buf *syncBuffer, want string, edit func()) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		edit()
		if strings.Contains(buf.String(), want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("output = %q, never contained %q", buf.String(), want)
}

func waitForOutput(t *testing.T, buf *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("output = %q, never contained %q", buf.String(), want)
}

func TestDevReportsThroughItsRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	t.Run("a dev session writes a run log", func(t *testing.T) {
		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		var stdout, stderr syncBuffer
		if err := runDev(context.Background(), devDeps(), false, root, []string{"sh", "-c", "exit 0"}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDev err = %v; stderr=%s", err, stderr.String())
		}

		logs, err := filepath.Glob(filepath.Join(root, statedir.Name, "runs", "*.ndjson"))
		if err != nil || len(logs) != 1 {
			t.Fatalf("run logs = %v (%v), want one", logs, err)
		}
		var resolved, summarized bool
		for _, ev := range clitest.RunEvents(t, readTestFile(t, logs[0])) {
			resolved = resolved || ev.GetEnded().GetTitle() == environmentTitle.Ended
			summarized = summarized || ev.GetSummary().GetSuccess()
		}
		if !resolved || !summarized {
			t.Errorf("run log records resolved=%v summarized=%v, want the resolved environment and a successful summary", resolved, summarized)
		}
	})

	t.Run("a re-resolve that fails is a failed span, not a raw print", func(t *testing.T) {
		root := t.TempDir()
		t.Cleanup(func() { _ = leader.Release(root) })
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"API_TOKEN","class":"VARIABLE_CLASS_PLAIN","required":true}`))
		clitest.WriteFile(t, filepath.Join(root, dotfile.FileName), "API_TOKEN=first\n")

		deps := devDeps()
		deps.LogFormat = terminal.FormatJSON
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var stdout, stderr syncBuffer
		done := make(chan error, 1)
		go func() {
			done <- runDev(ctx, deps, false, root, []string{"sleep", "10"}, &stdout, &stderr, strings.NewReader(""))
		}()

		waitForOutput(t, &stderr, environmentTitle.Ended)
		clitest.WriteFile(t, filepath.Join(root, dotfile.FileName), "# the value the run needs, deleted\n")
		waitForOutput(t, &stderr, "SPAN_STATUS_ERROR")

		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("runDev did not exit after cancellation")
		}

		var failed bool
		for _, ev := range clitest.RunEvents(t, stderr.String()) {
			if ev.GetEnded().GetStatus() == progressv1.SpanStatus_SPAN_STATUS_ERROR && strings.Contains(ev.GetMessage(), "API_TOKEN") {
				failed = true
			}
		}
		if !failed {
			t.Errorf("run events = %s, want a failed span naming API_TOKEN", stderr.String())
		}
	})

	t.Run("with --log-format json every line the run writes parses and the child's stdout is its own", func(t *testing.T) {
		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		deps := devDeps()
		deps.LogFormat = terminal.FormatJSON
		var stdout, stderr bytes.Buffer
		if err := runRun(context.Background(), deps, root, []string{"sh", "-c", "echo hello"}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runRun err = %v; stderr=%s", err, stderr.String())
		}

		if stdout.String() != "hello\n" {
			t.Errorf("stdout = %q, want only the child's own output", stdout.String())
		}
		events := clitest.RunEvents(t, stderr.String())
		if len(events) == 0 || !events[len(events)-1].GetSummary().GetSuccess() {
			t.Errorf("run events = %s, want them to end in a successful summary", stderr.String())
		}
	})
}
