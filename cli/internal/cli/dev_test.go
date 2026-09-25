package cli

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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/devlock"
	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/devstack"
	"github.com/ocelhq/ocel/cli/internal/dotenv"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/resolve"
	"github.com/ocelhq/ocel/cli/internal/watcher"
	"github.com/ocelhq/ocel/pkg/constants"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/devstack/docker"
	"github.com/ocelhq/ocel/cli/internal/devstack/docker/dockertest"
)

func init() {
	watchDebounce = 20 * time.Millisecond
}

func TestMergeEnv(t *testing.T) {
	t.Parallel()

	t.Run("a resource outranks the project, and the project outranks the shell", func(t *testing.T) {
		t.Parallel()

		base := []string{"PATH=/bin", "SHARED=base"}
		values := map[string]string{"SHARED": "project", "PROJECT_ONLY": "p"}
		resources := []resolve.Resource{
			{Name: "main", Env: map[string]string{"SHARED": "resource", "OCEL_RESOURCE_POSTGRES_main": "conn"}},
		}

		got := toMap(mergeEnv(base, nil, values, resources, runtimeAccess{}, "", envgate.Scope{}))

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

		got := mergeEnv([]string{"PATH=/usr/bin"}, live, map[string]string{"PROJECT_ONLY": "p"}, nil, runtimeAccess{}, "", envgate.Scope{})

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

func TestReportLiveValues(t *testing.T) {
	t.Parallel()

	t.Run("a run with no live values says nothing", func(t *testing.T) {
		t.Parallel()

		var quiet bytes.Buffer
		reportLiveValues(&quiet, nil)
		if quiet.Len() != 0 {
			t.Errorf("reportLiveValues wrote %q for a run with no live values, want nothing", quiet.String())
		}
	})

	t.Run("it names every live key and says dev resolves them like any other value", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		reportLiveValues(&out, []string{"WEBHOOK_SECRET", "API_TOKEN"})
		got := out.String()
		for _, want := range []string{"API_TOKEN", "WEBHOOK_SECRET", "every other value", "bounded window"} {
			if !strings.Contains(got, want) {
				t.Errorf("reportLiveValues wrote %q, want it to mention %q", got, want)
			}
		}
	})
}

func TestRunDev(t *testing.T) {
	t.Run("with no config file it discovers, declares, syncs and spawns", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		deps := devDeps()

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		envDumpPath := filepath.Join(root, "env.out")
		appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), deps, false, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		t.Run("the child's exit code becomes the command's", func(t *testing.T) {
			var exitErr *exitsig.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("runDev err = %v, want *exitsig.ExitError; stderr=%s", err, stderr.String())
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
	})

	t.Run("it joins the watcher before it returns", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		deps := devDeps()

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), deps, false, root, []string{"sh", "-c", "exit 7"}, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitsig.ExitError
		if !errors.As(err, &exitErr) || exitErr.Code != 7 {
			t.Fatalf("runDev err = %v, want exit 7; stderr=%s", err, stderr.String())
		}

		for _, frame := range []string{
			"github.com/ocelhq/ocel/cli/internal/watcher.run",
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
		t.Cleanup(func() { _ = devlock.Remove(root) })

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

		waitForLockfile(t, root)

		envDumpPath := filepath.Join(root, "follower-env.out")
		followerAppArgs := []string{"sh", "-c", "env > " + envDumpPath + "; exit 9"}

		subdir := filepath.Join(root, "apps", "web")
		clitest.WriteFile(t, filepath.Join(subdir, "package.json"), `{"name":"web"}`)
		clitest.WriteFile(t, filepath.Join(subdir, "index.ts"), "export {};\n")

		var followerStdout, followerStderr bytes.Buffer
		err := runDev(context.Background(), deps, false, subdir, followerAppArgs, &followerStdout, &followerStderr, strings.NewReader(""))

		var exitErr *exitsig.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("follower runDev err = %v, want *exitsig.ExitError; stderr=%s", err, followerStderr.String())
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
			t.Fatalf("OCEL_RESOURCE_POSTGRES_main = %q, want it to carry a postgres link", raw)
		}

		if got, ok := env[constants.AppFolderEnvName]; !ok || got != "/web" {
			t.Errorf("follower %s = %q (present=%v), want the folder the app binds", constants.AppFolderEnvName, got, ok)
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
		t.Cleanup(func() { _ = devlock.Remove(firstClone) })
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(firstClone), "main.ts"), declareResourceScript("first"))

		secondClone := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(secondClone) })
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(secondClone), "main.ts"), declareResourceScript("second"))

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()

		leaderDone := make(chan error, 1)
		var leaderStdout, leaderStderr syncBuffer
		go func() {
			leaderDone <- runDev(leaderCtx, deps, false, firstClone, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
		}()

		waitForLockfile(t, firstClone)

		envDumpPath := filepath.Join(secondClone, "env.out")
		appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 9"}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), deps, false, secondClone, appCmd, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitsig.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("second clone runDev err = %v, want *exitsig.ExitError; stderr=%s", err, stderr.String())
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
		t.Cleanup(func() { _ = devlock.Remove(root) })

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

		waitForLockfile(t, root)

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
		t.Cleanup(func() { _ = devlock.Remove(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"API_TOKEN","class":"VARIABLE_CLASS_PLAIN","required":true}`))
		clitest.WriteFile(t, filepath.Join(root, dotenv.FileName), "API_TOKEN=first\n")

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()

		var leaderStdout, leaderStderr syncBuffer
		leaderDone := make(chan error, 1)
		go func() {
			leaderDone <- runDev(leaderCtx, deps, false, root, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
		}()

		waitForLockfile(t, root)

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

		clitest.WriteFile(t, filepath.Join(root, dotenv.FileName), "API_TOKEN=second\n")

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

	t.Run("a refusal the edit fixes stops refusing", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"API_TOKEN","class":"VARIABLE_CLASS_PLAIN","required":true}`))
		clitest.WriteFile(t, filepath.Join(root, dotenv.FileName), "API_TOKEN=first\n")

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()

		var leaderStdout, leaderStderr syncBuffer
		leaderDone := make(chan error, 1)
		go func() {
			leaderDone <- runDev(leaderCtx, deps, false, root, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
		}()

		waitForLockfile(t, root)

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

		clitest.WriteFile(t, filepath.Join(root, dotenv.FileName), "# the value the run needs, deleted\n")
		waitForOutput(t, &leaderStderr, "API_TOKEN")
		if got := leaderStderr.String(); !strings.Contains(got, dotenv.FileName) {
			t.Errorf("stderr = %q, want the mid-session refusal to name %s", got, dotenv.FileName)
		}

		clitest.WriteFile(t, filepath.Join(root, dotenv.FileName), "API_TOKEN=restored\n")
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
		startWatching = func(ctx context.Context, srv *devserver.Server, cfg *projectconfig.Config, run invocation, stdout, stderr io.Writer) (*watcher.Watcher, error) {
			time.Sleep(300 * time.Millisecond)
			return stalled(ctx, srv, cfg, run, stdout, stderr)
		}
		t.Cleanup(func() { startWatching = stalled })

		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"API_TOKEN","class":"VARIABLE_CLASS_PLAIN","required":true}`))
		clitest.WriteFile(t, filepath.Join(root, dotenv.FileName), "API_TOKEN=first\n")

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()

		var leaderStdout, leaderStderr syncBuffer
		leaderDone := make(chan error, 1)
		go func() {
			leaderDone <- runDev(leaderCtx, deps, false, root, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
		}()

		waitForLockfile(t, root)

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

		clitest.WriteFile(t, filepath.Join(root, dotenv.FileName), "API_TOKEN=second\n")
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
		t.Cleanup(func() { _ = devlock.Remove(root) })

		deps := devDeps()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"API_TOKEN","class":"VARIABLE_CLASS_PLAIN","required":true}`))
		clitest.WriteFile(t, filepath.Join(root, dotenv.FileName), "API_TOKEN=first\n")

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()

		var leaderStdout, leaderStderr syncBuffer
		leaderDone := make(chan error, 1)
		go func() {
			leaderDone <- runDev(leaderCtx, deps, false, root, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
		}()

		waitForLockfile(t, root)

		waitForOutputAfter(t, &leaderStdout, "line 2", func() {
			clitest.WriteFile(t, filepath.Join(root, dotenv.FileName), "API_TOKEN=first\nnot a pair\n")
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
		t.Cleanup(func() { _ = devlock.Remove(root) })

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		srv := devserver.New("http://"+listener.Addr().String(), devstack.New("a-leader", devstack.Env{}))
		srv.PushEnv(map[string]string{"OCEL_RESOURCE_POSTGRES_main": `{"name":"main","postgres":{"host":"resolved","port":5432,"database":"main","username":"u","password":"p"}}`})

		httpSrv := &http.Server{Handler: srv.Mux()}
		go httpSrv.Serve(listener)

		if err := devlock.Create(root, devlock.Lease{Addr: listener.Addr().String(), Token: srv.AppToken()}); err != nil {
			t.Fatalf("devlock.Create: %v", err)
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

		waitForFile(t, startedPath)

		if err := httpSrv.Close(); err != nil {
			t.Fatalf("close fake leader: %v", err)
		}

		select {
		case err := <-followerDone:
			var exitErr *exitsig.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("follower runDev err = %v, want *exitsig.ExitError; stderr=%s", err, stderr.String())
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
	})
}

func TestDevSuppliesDeclaredResourcesItself(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	t.Run("a declared postgres comes from the dev stack, says where it landed, and stops with the run", func(t *testing.T) {
		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		engine := &dockertest.Engine{}
		deps := devDeps()
		deps.OpenDocker = engine.Opener()

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
		if !strings.Contains(stdout.String(), `postgres "main" → postgres:17 @ 127.0.0.1:`) {
			t.Errorf("stdout = %q, want one line saying where postgres \"main\" landed", stdout.String())
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
		t.Cleanup(func() { _ = devlock.Remove(root) })

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
		t.Cleanup(func() { _ = devlock.Remove(root) })
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
		t.Cleanup(func() { _ = devlock.Remove(root) })

		engine := &dockertest.Engine{}
		deps := devDeps()
		deps.OpenDocker = engine.Opener()

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), deps, true, root, []string{"sh", "-c", "exit 0"}, &stdout, &stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("runDev err = %v; stderr=%s", err, stderr.String())
		}
		if len(engine.Wiped) != 1 || engine.Wiped[0]["dev.ocel.project"] == "" || len(engine.Wiped[0]) != 1 {
			t.Fatalf("wiped %v, want exactly this project's volumes", engine.Wiped)
		}
	})

	t.Run("`ocel run` standing alone gets the same resources", func(t *testing.T) {
		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		engine := &dockertest.Engine{}
		deps := devDeps()
		deps.OpenDocker = engine.Opener()

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
			t.Errorf("command env carries no OCEL_RESOURCE_POSTGRES_main: %s", dumped)
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
		t.Cleanup(func() { _ = devlock.Remove(root) })
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
		deps.OpenDocker = engine.Opener()

		var stdout, stderr syncBuffer
		if err := runDev(ctx, deps, false, root, []string{"sh", "-c", "exit 0"}, &stdout, &stderr, strings.NewReader("")); err == nil {
			t.Fatal("runDev = nil for a startup that was interrupted")
		}
		if len(engine.Specs) != 1 || len(engine.Stopped) != 1 {
			t.Fatalf("ran %d containers and stopped %v, want the one that was started stopped; stderr=%s", len(engine.Specs), engine.Stopped, stderr.String())
		}
	})

	t.Run("the leader keeps its lease until its containers are stopped", func(t *testing.T) {
		root := t.TempDir()
		t.Cleanup(func() { _ = devlock.Remove(root) })
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))

		var leaseAtStop error
		engine := stopWatchingEngine{Engine: &dockertest.Engine{}, onStop: func() { _, leaseAtStop = devlock.Read(root) }}
		deps := devDeps()
		deps.OpenDocker = func(context.Context) (docker.Engine, error) { return engine, nil }

		var stdout, stderr syncBuffer
		if err := runDev(context.Background(), deps, false, root, []string{"sh", "-c", "exit 0"}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDev err = %v; stderr=%s", err, stderr.String())
		}
		if len(engine.Stopped) != 1 {
			t.Fatalf("stopped %v, want the run's container stopped", engine.Stopped)
		}
		if leaseAtStop != nil {
			t.Fatalf("the lease was already gone while the container was being stopped: %v", leaseAtStop)
		}
		if _, err := devlock.Read(root); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("devlock.Read after exit = %v, want the lease released", err)
		}
	})

	t.Run("an interrupted run has time to stop its containers before the hard exit", func(t *testing.T) {
		if docker.StopsWithin != 6*time.Second {
			t.Errorf("docker.StopsWithin = %s, want 6s: a 3s grace, then docker's kill and the removal", docker.StopsWithin)
		}
		if devStackStopsWithin < docker.StopsWithin {
			t.Errorf("the stack is given %s to stop and one container may take %s", devStackStopsWithin, docker.StopsWithin)
		}
		if spent := appChildWaitDelay + devStackStopsWithin; devShutdownWindow < spent+time.Second {
			t.Errorf("the hard exit lands %s after the interrupt, and the app child then the stack may take %s", devShutdownWindow, spent)
		}
		if devShutdownWindow != 14*time.Second {
			t.Errorf("devShutdownWindow = %s, want 14s", devShutdownWindow)
		}
	})
}

func TestTheAppsOriginsFollowThePortInTheDotfile(t *testing.T) {
	root := t.TempDir()
	origins := devAppOrigins(root, devSource{id: "dotenv"})

	clitest.WriteFile(t, filepath.Join(root, dotenv.FileName), "PORT=4100\n")
	if got := origins(); !slices.Contains(got, "http://localhost:4100") || !slices.Contains(got, "http://127.0.0.1:4100") {
		t.Fatalf("origins = %v, want the app on port 4100", got)
	}
	clitest.WriteFile(t, filepath.Join(root, dotenv.FileName), "PORT=4200\n")
	if got := origins(); !slices.Contains(got, "http://localhost:4200") || slices.Contains(got, "http://localhost:4100") {
		t.Fatalf("origins = %v after the port moved to 4200", got)
	}
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

func devDeps() cmddeps.Deps {
	deps := newDeps()
	deps.OpenDocker = (&dockertest.Engine{}).Opener()
	return deps
}

func waitForLockfile(t *testing.T, root string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := devlock.Read(root); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("lockfile for %q never appeared", root)
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
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + process.env.`+constants.DevServerTokenEnvName+` },
    body: JSON.stringify({
      resource: { type: "RESOURCE_TYPE_POSTGRES", name: %q },
      postgres: { version: "17" },
    }),
  }),
);
export {};
`, constants.DevServerEnvName, name)
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
