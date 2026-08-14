package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/consolebinding"
	"github.com/ocelhq/ocel/cli/internal/credentials"
	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/dotenv"
	"github.com/ocelhq/ocel/cli/internal/lockfile"
	"github.com/ocelhq/ocel/cli/internal/provision"
)

func TestMergeEnv(t *testing.T) {
	t.Parallel()

	t.Run("a resource outranks the project, and the project outranks the shell", func(t *testing.T) {
		t.Parallel()

		base := []string{"PATH=/bin", "SHARED=base"}
		projectEnv := map[string]string{"SHARED": "project", "PROJECT_ONLY": "p"}
		resources := []provision.Resource{
			{Name: "main", Env: map[string]string{"SHARED": "resource", "OCEL_RESOURCE_POSTGRES_main": "conn"}},
		}

		got := toMap(mergeEnv(base, projectEnv, nil, nil, resources, ""))

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

		got := mergeEnv([]string{"PATH=/usr/bin"}, map[string]string{"PROJECT_ONLY": "p"}, live, nil, nil, "")

		for _, kv := range got {
			if strings.HasPrefix(kv, "OCEL_LIVE_KEYS=") {
				t.Errorf("dev set %q; there is no membrane here to send the push it promises", kv)
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

	t.Run("it names every live key and says dev resolves them once", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		reportLiveValues(&out, []string{"WEBHOOK_SECRET", "API_TOKEN"})
		got := out.String()
		for _, want := range []string{"API_TOKEN", "WEBHOOK_SECRET", "once", "restart"} {
			if !strings.Contains(got, want) {
				t.Errorf("reportLiveValues wrote %q, want it to mention %q", got, want)
			}
		}
	})
}

func TestRunDev(t *testing.T) {
	t.Run("not logged in returns an exit error pointing at `ocel login`", func(t *testing.T) {
		d := defaultDeps()
		d.loadCredentials = func() (credentials.Credentials, error) {
			return credentials.Credentials{}, credentials.ErrNotLoggedIn
		}

		var stderr bytes.Buffer
		err := runDev(context.Background(), d, nil, t.TempDir(), []string{"true"}, &bytes.Buffer{}, &stderr, strings.NewReader(""))

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
	})

	t.Run("an unlinked directory with no terminal errors toward `ocel console link`", func(t *testing.T) {
		d := defaultDeps()
		setLoggedIn(&d)

		var stdout, stderr bytes.Buffer
		err := runDev(context.Background(), d, nil, t.TempDir(), []string{"true"}, &stdout, &stderr, strings.NewReader(""))

		if err == nil {
			t.Fatal("runDev: expected an error for an unlinked directory, got nil")
		}
		if !strings.Contains(err.Error(), "ocel console link") {
			t.Fatalf("err = %q, want it to point at `ocel console link`", err.Error())
		}
	})

	t.Run("a directory linked to another control plane errors toward `ocel console link`", func(t *testing.T) {
		d := defaultDeps()
		setLoggedIn(&d)

		root := t.TempDir()
		writeLink(t, root, "https://elsewhere.example.com", "proj_elsewhere")

		err := runDev(context.Background(), d, nil, root, []string{"true"}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))

		if err == nil || !strings.Contains(err.Error(), "ocel console link") {
			t.Fatalf("runDev err = %v, want it to point at `ocel console link`", err)
		}
	})

	t.Run("with no config file it discovers, declares, syncs and spawns", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		d := defaultDeps()
		withCredentials(&d, resolveServer.URL)

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		writeLink(t, root, resolveServer.URL, testProjectID(t))
		writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareResourceScript("main"))

		envDumpPath := filepath.Join(root, "env.out")
		appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), d, nil, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		t.Run("the child's exit code becomes the command's", func(t *testing.T) {
			var exitErr *ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("runDev err = %v, want *ExitError; stderr=%s", err, stderr.String())
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
	})

	t.Run("a second run in the same root from a subdirectory becomes a follower and receives the pushed env", func(t *testing.T) {
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
export default { slug: "test-app", apps: [{ name: "web", path: "apps/web", folder: "/web" }] };
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

		envDumpPath := filepath.Join(root, "follower-env.out")
		followerAppArgs := []string{"sh", "-c", "env > " + envDumpPath + "; exit 9"}

		subdir := filepath.Join(root, "apps", "web")
		writeFile(t, filepath.Join(subdir, "index.ts"), "export {};\n")

		var followerStdout, followerStderr bytes.Buffer
		err := runDev(context.Background(), d, nil, subdir, followerAppArgs, &followerStdout, &followerStderr, strings.NewReader(""))

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

		if got, ok := env["OCEL_APP_FOLDER"]; !ok || got != "/web" {
			t.Errorf("follower OCEL_APP_FOLDER = %q (present=%v), want the folder the app binds", got, ok)
		}

		cancelLeader()
		select {
		case <-leaderDone:
		case <-time.After(5 * time.Second):
			t.Fatal("leader runDev did not exit after cancellation")
		}
	})

	t.Run("a second root linked to the same project elects its own leader", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		d := defaultDeps()
		withCredentials(&d, resolveServer.URL)

		projectID := "proj_" + t.Name()

		firstClone := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(firstClone) })
		writeLink(t, firstClone, resolveServer.URL, projectID)
		writeFile(t, filepath.Join(firstClone, "ocel", "main.ts"), declareResourceScript("first"))

		secondClone := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(secondClone) })
		writeLink(t, secondClone, resolveServer.URL, projectID)
		writeFile(t, filepath.Join(secondClone, "ocel", "main.ts"), declareResourceScript("second"))

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()

		leaderDone := make(chan error, 1)
		var leaderStdout, leaderStderr syncBuffer
		go func() {
			leaderDone <- runDev(leaderCtx, d, nil, firstClone, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
		}()

		waitForLockfile(t, firstClone)

		envDumpPath := filepath.Join(secondClone, "env.out")
		appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 9"}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), d, nil, secondClone, appCmd, &stdout, &stderr, strings.NewReader(""))

		var exitErr *ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("second clone runDev err = %v, want *ExitError; stderr=%s", err, stderr.String())
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

		prevDebounce := watchDebounce
		watchDebounce = 20 * time.Millisecond
		defer func() { watchDebounce = prevDebounce }()

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

		var leaderStdout, leaderStderr syncBuffer

		leaderDone := make(chan error, 1)
		go func() {
			leaderDone <- runDev(leaderCtx, d, nil, root, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
		}()

		waitForLockfile(t, root)

		envDumpPath := filepath.Join(root, "follower-env.out")
		followerAppArgs := []string{"sh", "-c", "while true; do env > " + envDumpPath + "; sleep 0.02; done"}

		followerCtx, cancelFollower := context.WithCancel(context.Background())
		defer cancelFollower()
		followerDone := make(chan error, 1)
		var followerStdout, followerStderr bytes.Buffer
		go func() {
			followerDone <- runDev(followerCtx, d, nil, root, followerAppArgs, &followerStdout, &followerStderr, strings.NewReader(""))
		}()

		waitForEnvVar(t, envDumpPath, "OCEL_RESOURCE_POSTGRES_main")

		writeFile(t, filepath.Join(root, "ocel", "second.ts"), declareResourceScript("second"))

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

		prevDebounce := watchDebounce
		watchDebounce = 20 * time.Millisecond
		defer func() { watchDebounce = prevDebounce }()

		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		d := defaultDeps()
		withCredentials(&d, resolveServer.URL)
		writeLink(t, root, resolveServer.URL, testProjectID(t))
		writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"API_TOKEN","class":"VARIABLE_CLASS_PLAIN","required":true}`))
		writeFile(t, filepath.Join(root, dotenv.FileName), "API_TOKEN=first\n")

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()

		var leaderStdout, leaderStderr syncBuffer
		leaderDone := make(chan error, 1)
		go func() {
			leaderDone <- runDev(leaderCtx, d, nil, root, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
		}()

		waitForLockfile(t, root)

		envDumpPath := filepath.Join(root, "follower-env.out")
		followerAppArgs := []string{"sh", "-c", "while true; do env > " + envDumpPath + "; sleep 0.02; done"}

		followerCtx, cancelFollower := context.WithCancel(context.Background())
		defer cancelFollower()
		followerDone := make(chan error, 1)
		var followerStdout, followerStderr bytes.Buffer
		go func() {
			followerDone <- runDev(followerCtx, d, nil, root, followerAppArgs, &followerStdout, &followerStderr, strings.NewReader(""))
		}()

		waitForEnvValue(t, envDumpPath, "API_TOKEN", "first")

		writeFile(t, filepath.Join(root, dotenv.FileName), "API_TOKEN=second\n")

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

		prevDebounce := watchDebounce
		watchDebounce = 20 * time.Millisecond
		defer func() { watchDebounce = prevDebounce }()

		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		d := defaultDeps()
		withCredentials(&d, resolveServer.URL)
		writeLink(t, root, resolveServer.URL, testProjectID(t))
		writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"API_TOKEN","class":"VARIABLE_CLASS_PLAIN","required":true}`))
		writeFile(t, filepath.Join(root, dotenv.FileName), "API_TOKEN=first\n")

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()

		var leaderStdout, leaderStderr syncBuffer
		leaderDone := make(chan error, 1)
		go func() {
			leaderDone <- runDev(leaderCtx, d, nil, root, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
		}()

		waitForLockfile(t, root)

		envDumpPath := filepath.Join(root, "follower-env.out")
		followerAppArgs := []string{"sh", "-c", "while true; do env > " + envDumpPath + "; sleep 0.02; done"}

		followerCtx, cancelFollower := context.WithCancel(context.Background())
		defer cancelFollower()
		followerDone := make(chan error, 1)
		var followerStdout, followerStderr bytes.Buffer
		go func() {
			followerDone <- runDev(followerCtx, d, nil, root, followerAppArgs, &followerStdout, &followerStderr, strings.NewReader(""))
		}()

		waitForEnvValue(t, envDumpPath, "API_TOKEN", "first")

		writeFile(t, filepath.Join(root, dotenv.FileName), "# the value the run needs, deleted\n")
		waitForOutput(t, &leaderStderr, "API_TOKEN")
		if got := leaderStderr.String(); !strings.Contains(got, dotenv.FileName) {
			t.Errorf("stderr = %q, want the mid-session refusal to name %s", got, dotenv.FileName)
		}

		writeFile(t, filepath.Join(root, dotenv.FileName), "API_TOKEN=restored\n")
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

	t.Run("an edit that introduces an unreadable line says so", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX shell fixture command")
		}

		prevDebounce := watchDebounce
		watchDebounce = 20 * time.Millisecond
		defer func() { watchDebounce = prevDebounce }()

		resolveServer := newFakeResolveServer(t)
		defer resolveServer.Close()

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		d := defaultDeps()
		withCredentials(&d, resolveServer.URL)
		writeLink(t, root, resolveServer.URL, testProjectID(t))
		writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareEnvScript(`{"key":"API_TOKEN","class":"VARIABLE_CLASS_PLAIN","required":true}`))
		writeFile(t, filepath.Join(root, dotenv.FileName), "API_TOKEN=first\n")

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		defer cancelLeader()

		var leaderStdout, leaderStderr syncBuffer
		leaderDone := make(chan error, 1)
		go func() {
			leaderDone <- runDev(leaderCtx, d, nil, root, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
		}()

		waitForLockfile(t, root)

		waitForOutputAfter(t, &leaderStdout, "line 2", func() {
			writeFile(t, filepath.Join(root, dotenv.FileName), "API_TOKEN=first\nnot a pair\n")
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

		d := defaultDeps()
		setLoggedIn(&d)

		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		projectID := "proj_" + t.Name()
		const apiURL = "https://api.example.com"
		srv := devserver.New(apiURL, "tok", projectID, "http://127.0.0.1:0")
		srv.PushEnv(map[string]string{"OCEL_RESOURCE_POSTGRES_main": `{"connectionString":"conn"}`})

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		httpSrv := &http.Server{Handler: srv.Mux()}
		go httpSrv.Serve(listener)

		if err := lockfile.Create(root, listener.Addr().String()); err != nil {
			t.Fatalf("lockfile.Create: %v", err)
		}

		writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
		writeLink(t, root, apiURL, projectID)

		startedPath := filepath.Join(root, "started")
		appArgs := []string{"sh", "-c", "touch " + startedPath + "; sleep 10"}

		followerDone := make(chan error, 1)
		var stdout, stderr bytes.Buffer
		go func() {
			followerDone <- runDev(context.Background(), d, nil, root, appArgs, &stdout, &stderr, strings.NewReader(""))
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
	})
}

func testProjectID(t *testing.T) string {
	t.Helper()
	return "proj_" + strings.ReplaceAll(t.Name(), "/", "_")
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

func newFakeResolveServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/resources/resolve" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Resources []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"resources"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		env := make(map[string]string, len(req.Resources))
		for _, r := range req.Resources {
			key := fmt.Sprintf("OCEL_RESOURCE_%s_%s", r.Type, r.Name)
			env[key] = fmt.Sprintf(`{"connectionString":"postgres://resolved/%s"}`, r.Name)
		}

		json.NewEncoder(w).Encode(map[string]any{
			"env":       env,
			"expiresAt": time.Now().Add(time.Hour).Format(time.RFC3339),
		})
	}))
}

func waitForLockfile(t *testing.T, root string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := lockfile.Read(root); err == nil {
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
  fetch(new URL("/resources.v1.ResourceService/Declare", process.env.OCEL_DEV_SERVER), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      resource: { type: "RESOURCE_TYPE_POSTGRES", name: %q },
      postgres: { version: "17" },
    }),
  }),
);
export {};
`, name)
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

func writeLink(t *testing.T, dir, apiURL, projectID string) {
	t.Helper()
	link := consolebinding.Binding{APIURL: apiURL, OrganizationID: "org_1", ProjectID: projectID, ProjectName: "Test"}
	if err := consolebinding.Write(dir, link); err != nil {
		t.Fatalf("write link: %v", err)
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
