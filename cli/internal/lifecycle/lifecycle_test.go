//go:build unix

package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/processenv"
)

func TestACommandReadsItsLiveValuesFromFilesUnderTheLiveDirAndNotFromItsEnvironment(t *testing.T) {
	t.Setenv("OCEL_RESOURCE_POSTGRES_inherited", "leaked")
	out := filepath.Join(t.TempDir(), "seen")
	var stdout bytes.Buffer

	err := Run(context.Background(), Command{
		Line:   `cat "$OCEL_LIVE_DIR/OCEL_RESOURCE_POSTGRES_main" > "$SEEN"; echo >> "$SEEN"; echo "$GREETING" >> "$SEEN"; echo "[${OCEL_RESOURCE_POSTGRES_main:-unset}|${OCEL_RESOURCE_POSTGRES_inherited:-unset}]" >> "$SEEN"; echo "$OCEL_LIVE_DIR" >> "$SEEN"`,
		Dir:    t.TempDir(),
		Env:    map[string]string{"GREETING": "hello", "SEEN": out},
		Live:   map[string]string{"OCEL_RESOURCE_POSTGRES_main": `{"name":"main"}`},
		Stdout: &stdout,
		Stderr: &stdout,
	})
	if err != nil {
		t.Fatalf("Run err = %v, want nil; output %q", err, stdout.String())
	}

	lines := strings.Split(strings.TrimSpace(readFile(t, out)), "\n")
	if len(lines) != 4 || lines[0] != `{"name":"main"}` || lines[1] != "hello" || lines[2] != "[unset|unset]" {
		t.Fatalf("the command saw %q, want its live value from the live dir, its env, and no OCEL_RESOURCE_ variable in its environment", lines)
	}
	if _, err := os.Stat(lines[3]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the live dir %s survives the command (stat err %v), want it removed", lines[3], err)
	}
}

func TestACommandWithNoLiveValuesIsHandedNoLiveDir(t *testing.T) {
	out := filepath.Join(t.TempDir(), "seen")

	err := Run(context.Background(), Command{
		Line: `echo "[${OCEL_LIVE_DIR-unset}]" > "$SEEN"`,
		Dir:  t.TempDir(),
		Env:  map[string]string{"SEEN": out},
	})
	if err != nil {
		t.Fatalf("Run err = %v", err)
	}
	if got := strings.TrimSpace(readFile(t, out)); got != "[unset]" {
		t.Errorf("the command saw %s, want none", got)
	}
}

func TestACommandRunsInTheDirectoryItIsGiven(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(t.TempDir(), "seen")

	if err := Run(context.Background(), Command{Line: `pwd -P > "$SEEN"`, Dir: dir, Env: map[string]string{"SEEN": out}}); err != nil {
		t.Fatalf("Run err = %v", err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(readFile(t, out)); got != resolved {
		t.Errorf("the command ran in %s, want %s", got, resolved)
	}
}

func TestACommandsOutputReachesTheWriterAsItIsPrinted(t *testing.T) {
	var out bytes.Buffer

	if err := Run(context.Background(), Command{Line: `echo to-stdout; echo to-stderr >&2`, Dir: t.TempDir(), Stdout: &out, Stderr: &out}); err != nil {
		t.Fatalf("Run err = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "to-stdout") || !strings.Contains(got, "to-stderr") {
		t.Errorf("output = %q, want both streams", got)
	}
}

func TestACommandThatExitsNonZeroFailsWithItsExitCode(t *testing.T) {
	err := Run(context.Background(), Command{Line: `exit 3`, Dir: t.TempDir()})

	var failed *NonZeroExitError
	if !errors.As(err, &failed) || failed.ExitCode != 3 {
		t.Fatalf("Run err = %v, want a NonZeroExitError with exit code 3", err)
	}
}

func TestACommandThatRunsPastItsTimeoutIsStoppedWithItsWholeProcessGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "survived")
	started := time.Now()

	err := Run(context.Background(), Command{
		Line:    `(sleep 3; touch "$MARKER") & wait`,
		Dir:     t.TempDir(),
		Env:     map[string]string{"MARKER": marker},
		Timeout: 200 * time.Millisecond,
	})

	var timedOut *TimeoutError
	if !errors.As(err, &timedOut) || timedOut.After != 200*time.Millisecond {
		t.Fatalf("Run err = %v, want a TimeoutError after 200ms", err)
	}
	if took := time.Since(started); took > 2500*time.Millisecond {
		t.Errorf("Run took %s, want it to return once the command was stopped", took)
	}
	time.Sleep(3500 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a process the command started outlived the timeout (stat err %v), want the whole group stopped", err)
	}
}

func TestACommandStoppedByItsCallerReportsTheCancellationAndNotATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	err := Run(ctx, Command{Line: `sleep 5`, Dir: t.TempDir(), Timeout: time.Minute})

	var timedOut *TimeoutError
	if err == nil || errors.As(err, &timedOut) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want the cancellation", err)
	}
}

func TestTheLiveDirNeverReachesTheProcessEnvironmentUnderTheReservedName(t *testing.T) {
	t.Setenv(processenv.LiveDirEnvVar, "/inherited")
	out := filepath.Join(t.TempDir(), "seen")

	err := Run(context.Background(), Command{
		Line: `echo "$OCEL_LIVE_DIR" > "$SEEN"`,
		Dir:  t.TempDir(),
		Env:  map[string]string{"SEEN": out},
		Live: map[string]string{"KEY": "v"},
	})
	if err != nil {
		t.Fatalf("Run err = %v", err)
	}
	if got := strings.TrimSpace(readFile(t, out)); got == "/inherited" || got == "" {
		t.Errorf("OCEL_LIVE_DIR = %q, want the dir written for this command", got)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	read, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(read)
}
