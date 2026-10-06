//go:build unix

package host

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const standInInspect = `#!/bin/sh
[ "$1" = inspect ] || exit 1
for name; do :; done
cat "STATE/$name" 2>/dev/null || exit 1
`

func watchedOn(t *testing.T, states map[string]string) (localShell, string) {
	t.Helper()
	bin, state := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(strings.ReplaceAll(standInInspect, "STATE", state)), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, said := range states {
		if err := os.WriteFile(filepath.Join(state, name), []byte(said+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return localShell{t: t, path: bin}, state
}

func TestTheWatchStopsTheProbeAndNamesTheContainerOnceItCrashLoops(t *testing.T) {
	t.Parallel()

	box, state := watchedOn(t, map[string]string{"web": "running 0", "api": "restarting " + strconv.Itoa(crashLoopRestarts)})
	pidFile := filepath.Join(state, "probe.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	began := time.Now()
	said, err := box.Stream(ctx, renderStartWatch([]string{"web", "api"}, []string{"sh", "-c", "echo $$ > " + pidFile + "; exec sleep 300"}), nil)
	if err != nil {
		t.Fatalf("the watch = %v", err)
	}
	if took := time.Since(began); took > 10*time.Second {
		t.Errorf("the watch took %s to give up on a crash-looping container, want it to stop the probe rather than wait it out", took)
	}
	if name, crashed := findUnstarted(said.Stdout); !crashed || name != "api" {
		t.Errorf("the watch said %q, want it to name api as unstarted", said.Stdout)
	}
	if said.Code == 0 {
		t.Error("the watch exited 0 over a container that never started")
	}
	written, err := os.ReadFile(pidFile)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(written)))
	for deadline := time.Now().Add(5 * time.Second); processRunning(pid) && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if processRunning(pid) {
		t.Errorf("the probe (pid %d) was still running after the watch gave up on its container", pid)
	}
}

func TestTheWatchHandsBackWhatTheProbeSaidWhileTheContainerHasNotCrashLooped(t *testing.T) {
	t.Parallel()

	for status, code := range map[string]int{"running 0": 0, "restarting " + strconv.Itoa(crashLoopRestarts-1): 4} {
		box, _ := watchedOn(t, map[string]string{"web": status})
		probe := []string{"sh", "-c", "sleep 1; echo answered /up 503; echo never answered >&2; exit " + strconv.Itoa(code)}
		said, err := box.Stream(context.Background(), renderStartWatch([]string{"web"}, probe), nil)
		if err != nil {
			t.Fatalf("%s: the watch = %v", status, err)
		}
		if said.Code != code || said.Stdout != "answered /up 503\n" || said.Stderr != "never answered\n" {
			t.Errorf("%s: the watch handed back %+v, want the probe's own output and exit %d", status, said, code)
		}
		if _, crashed := findUnstarted(said.Stdout); crashed {
			t.Errorf("%s: the watch named a container unstarted that has not crash-looped", status)
		}
	}
}
