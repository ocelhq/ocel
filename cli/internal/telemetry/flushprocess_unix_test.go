//go:build unix

package telemetry_test

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ocelhq/ocel/cli/internal/clitest/confighome"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
	"github.com/ocelhq/ocel/cli/internal/userconfig"
)

const flushChildEnvVar = "OCEL_TEST_FLUSH_CHILD"

type flushChild struct {
	Args       []string `json:"args"`
	PID        int      `json:"pid"`
	SessionID  int      `json:"session_id"`
	GroupID    int      `json:"group_id"`
	NullStdin  bool     `json:"null_stdin"`
	NullStdout bool     `json:"null_stdout"`
	NullStderr bool     `json:"null_stderr"`
}

func init() {
	dir := os.Getenv(flushChildEnvVar)
	if dir == "" {
		return
	}
	sid, _ := unix.Getsid(0)
	isNull := func(f *os.File) bool {
		info, err := f.Stat()
		null, nullErr := os.Stat(os.DevNull)
		return err == nil && nullErr == nil && os.SameFile(info, null)
	}
	raw, _ := json.Marshal(flushChild{
		Args: os.Args[1:], PID: os.Getpid(), SessionID: sid, GroupID: syscall.Getpgrp(),
		NullStdin: isNull(os.Stdin), NullStdout: isNull(os.Stdout), NullStderr: isNull(os.Stderr),
	})
	_ = os.WriteFile(filepath.Join(dir, "child.json"), raw, 0o600)
	time.Sleep(time.Second)
	_ = os.WriteFile(filepath.Join(dir, "finished"), nil, 0o600)
	os.Exit(0)
}

func aFlushableSetup(t *testing.T) (childDir string) {
	t.Helper()
	confighome.Isolate(t)
	withBuildValues(t, "a-key", "http://127.0.0.1:1")
	telemetry.PrintBannerOnce(io.Discard, telemetry.Resolution{Enabled: true})
	spool, err := telemetry.OpenSpool()
	if err != nil {
		t.Fatal(err)
	}
	if err := spool.Append(aCompletedEvent(t, "deploy")); err != nil {
		t.Fatal(err)
	}
	childDir = t.TempDir()
	t.Setenv(flushChildEnvVar, childDir)
	t.Cleanup(func() {
		if fileExists(filepath.Join(childDir, "child.json")) {
			waitForFile(filepath.Join(childDir, "finished"))
		}
	})
	return childDir
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func waitForFile(path string) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fileExists(path) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func readFlushChild(t *testing.T, dir string) flushChild {
	t.Helper()
	if !waitForFile(filepath.Join(dir, "child.json")) {
		t.Fatal("the flush child never started")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "child.json"))
	if err != nil {
		t.Fatal(err)
	}
	var child flushChild
	if err := json.Unmarshal(raw, &child); err != nil {
		t.Fatal(err)
	}
	return child
}

func TestStartFlushRunsTelemetryFlushDetachedWithoutWaitingForIt(t *testing.T) {
	dir := aFlushableSetup(t)
	started := time.Now()

	if !telemetry.StartFlush(os.Args[0], telemetry.Resolution{Enabled: true}) {
		t.Fatal("StartFlush() = false, want the child started")
	}

	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Errorf("StartFlush() took %s, want it to return without waiting for the child", elapsed)
	}
	if fileExists(filepath.Join(dir, "finished")) {
		t.Error("the child had already finished when StartFlush returned")
	}
	child := readFlushChild(t, dir)
	if len(child.Args) != 2 || child.Args[0] != "telemetry" || child.Args[1] != "flush" {
		t.Errorf("child args = %v, want [telemetry flush]", child.Args)
	}
	if parentSession, _ := unix.Getsid(0); child.SessionID == parentSession || child.GroupID != child.PID {
		t.Errorf("child session %d (parent %d), group %d, pid %d, want its own session and process group", child.SessionID, parentSession, child.GroupID, child.PID)
	}
	if !child.NullStdin || !child.NullStdout || !child.NullStderr {
		t.Errorf("child null stdin/stdout/stderr = %v/%v/%v, want all on the null device", child.NullStdin, child.NullStdout, child.NullStderr)
	}
}

func TestStartFlushSkipsTheChildUnlessThereIsSomethingToSend(t *testing.T) {
	cases := map[string]func(t *testing.T){
		"spool is empty": func(t *testing.T) {
			cache, err := os.UserCacheDir()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(filepath.Join(cache, "ocel", "telemetry")); err != nil {
				t.Fatal(err)
			}
		},
		"no endpoint": func(t *testing.T) { withBuildValues(t, "a-key", "") },
		"no key":      func(t *testing.T) { withBuildValues(t, "", "http://127.0.0.1:1") },
		"opted out":   func(t *testing.T) { t.Setenv("OCEL_TELEMETRY", "0") },
		"banner not yet shown": func(t *testing.T) {
			dir, err := userconfig.Dir()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			dir := aFlushableSetup(t)
			spoil(t)

			resolution := telemetry.Resolve(telemetry.WriteKey, telemetry.Endpoint)
			if telemetry.StartFlush(os.Args[0], resolution) {
				t.Error("StartFlush() = true, want no child")
			}
			time.Sleep(100 * time.Millisecond)
			if fileExists(filepath.Join(dir, "child.json")) {
				t.Error("a flush child ran")
			}
		})
	}
}

func TestStartFlushNeverStartsInDebugMode(t *testing.T) {
	aFlushableSetup(t)

	if telemetry.StartFlush(os.Args[0], telemetry.Resolution{Enabled: true, Debug: true}) {
		t.Error("StartFlush() = true, want debug mode to send nothing")
	}
}

func TestStartFlushReportsAnExecutableThatCannotStart(t *testing.T) {
	dir := aFlushableSetup(t)

	if telemetry.StartFlush(filepath.Join(dir, "no-such-binary"), telemetry.Resolution{Enabled: true}) {
		t.Error("StartFlush() = true for an executable that does not exist")
	}
}
