package dev

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/dotfile"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
)

type sessionLog struct {
	mu       sync.Mutex
	sessions []telemetry.DevSession
}

func (l *sessionLog) record(session telemetry.DevSession) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sessions = append(l.sessions, session)
}

func (l *sessionLog) theOnlySession(t *testing.T) telemetry.DevSession {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.sessions) != 1 {
		t.Fatalf("recorded %d dev sessions, want exactly one: %+v", len(l.sessions), l.sessions)
	}
	return l.sessions[0]
}

func recordingDeps(log *sessionLog) testDeps {
	deps := devDeps()
	deps.RecordSession = log.record
	return deps
}

func stopAndWait(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runDev did not exit after cancellation")
	}
}

func TestAnInterruptedDevSessionRecordsItsDurationAndDeclaredKindsAndNoErrorCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}
	root := t.TempDir()
	t.Cleanup(func() { releaseLeader(root) })
	clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("orders"))
	var log sessionLog
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	envDumpPath := filepath.Join(root, "env.out")
	var stdout, stderr syncBuffer
	done := make(chan error, 1)
	go func() {
		done <- runDev(ctx, recordingDeps(&log), false, root, []string{"sh", "-c", "env > " + envDumpPath + "; sleep 10"}, &stdout, &stderr, strings.NewReader(""))
	}()
	waitForEnvVar(t, envDumpPath, "OCEL_RESOURCE_POSTGRES_orders")

	stopAndWait(t, cancel, done)

	session := log.theOnlySession(t)
	if session.Duration <= 0 || session.Reloads != 0 || !slices.Equal(session.ResourceKinds, []string{"postgres"}) || len(session.ErrorCodes) != 0 {
		t.Errorf("session = %+v, want a positive duration, no reloads, [postgres] and no error codes: an interrupt is how a session normally ends", session)
	}
}

func TestADevSessionCountsEachEnvironmentDrivenRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}
	root := t.TempDir()
	t.Cleanup(func() { releaseLeader(root) })
	clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))
	var log sessionLog
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	envDumpPath := filepath.Join(root, "env.out")
	var stdout, stderr syncBuffer
	done := make(chan error, 1)
	go func() {
		done <- runDev(ctx, recordingDeps(&log), false, root, []string{"sh", "-c", "env > " + envDumpPath + "; sleep 10"}, &stdout, &stderr, strings.NewReader(""))
	}()
	waitForEnvVar(t, envDumpPath, "OCEL_RESOURCE_POSTGRES_main")

	clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "second.ts"), declareResourceScript("second"))
	waitForEnvVar(t, envDumpPath, "OCEL_RESOURCE_POSTGRES_second")
	stopAndWait(t, cancel, done)

	if session := log.theOnlySession(t); session.Reloads != 1 {
		t.Errorf("reloads = %d, want 1", session.Reloads)
	}
}

func TestADevSessionCountsTheCodedRefusalsAReloadRaised(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}
	root := t.TempDir()
	t.Cleanup(func() { releaseLeader(root) })
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
	clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareEnvScript(`{"key":"API_TOKEN","class":"VARIABLE_CLASS_PLAIN","required":true}`))
	clitest.WriteFile(t, filepath.Join(root, dotfile.FileName), "API_TOKEN=first\n")
	var log sessionLog
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	envDumpPath := filepath.Join(root, "env.out")
	var stdout, stderr syncBuffer
	done := make(chan error, 1)
	go func() {
		done <- runDev(ctx, recordingDeps(&log), false, root, []string{"sh", "-c", "env > " + envDumpPath + "; sleep 10"}, &stdout, &stderr, strings.NewReader(""))
	}()
	waitForEnvValue(t, envDumpPath, "API_TOKEN", "first")

	clitest.WriteFile(t, filepath.Join(root, dotfile.FileName), "# the value the run needs, deleted\n")
	waitForOutput(t, &stderr, "not ready")
	clitest.WriteFile(t, filepath.Join(root, dotfile.FileName), "API_TOKEN=restored\n")
	waitForEnvValue(t, envDumpPath, "API_TOKEN", "restored")
	stopAndWait(t, cancel, done)

	session := log.theOnlySession(t)
	if got := session.ErrorCodes["variables.missing"]; got != 1 || len(session.ErrorCodes) != 1 {
		t.Errorf("error codes = %v, want exactly variables.missing once", session.ErrorCodes)
	}
	if session.Reloads != 1 {
		t.Errorf("reloads = %d, want 1: the refused reload restarted nothing", session.Reloads)
	}
	if len(session.ResourceKinds) != 0 {
		t.Errorf("resource kinds = %v, want none declared", session.ResourceKinds)
	}
}

func TestADevSessionWhoseCommandFailsCountsThatCodeOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}
	root := t.TempDir()
	t.Cleanup(func() { releaseLeader(root) })
	clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))
	var log sessionLog
	var stdout, stderr syncBuffer

	err := runDev(context.Background(), recordingDeps(&log), false, root, []string{"sh", "-c", "exit 7"}, &stdout, &stderr, strings.NewReader(""))

	var exit *exitcode.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("err = %v, want the child's exit", err)
	}
	if session := log.theOnlySession(t); session.ErrorCodes["dev.command_failed"] != 1 || len(session.ErrorCodes) != 1 {
		t.Errorf("error codes = %v, want exactly dev.command_failed once", session.ErrorCodes)
	}
}

func TestADevSessionFollowingALeaderCountsItsRestartsAndKnowsNoKinds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}
	root := t.TempDir()
	t.Cleanup(func() { releaseLeader(root) })
	clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareResourceScript("main"))
	var leaders, followers sessionLog
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	var leaderStdout, leaderStderr syncBuffer
	leaderDone := make(chan error, 1)
	go func() {
		leaderDone <- runDev(leaderCtx, recordingDeps(&leaders), false, root, []string{"sleep", "10"}, &leaderStdout, &leaderStderr, strings.NewReader(""))
	}()
	waitForLeaderRecord(t, root)
	envDumpPath := filepath.Join(root, "follower-env.out")
	followerCtx, cancelFollower := context.WithCancel(context.Background())
	defer cancelFollower()
	var followerStdout, followerStderr syncBuffer
	followerDone := make(chan error, 1)
	go func() {
		followerDone <- runDev(followerCtx, recordingDeps(&followers), false, root, []string{"sh", "-c", "while true; do env > " + envDumpPath + "; sleep 0.02; done"}, &followerStdout, &followerStderr, strings.NewReader(""))
	}()
	waitForEnvVar(t, envDumpPath, "OCEL_RESOURCE_POSTGRES_main")

	clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "second.ts"), declareResourceScript("second"))
	waitForEnvVar(t, envDumpPath, "OCEL_RESOURCE_POSTGRES_second")
	stopAndWait(t, cancelFollower, followerDone)
	stopAndWait(t, cancelLeader, leaderDone)

	follower, leader := followers.theOnlySession(t), leaders.theOnlySession(t)
	if follower.Reloads != 1 || len(follower.ResourceKinds) != 0 {
		t.Errorf("follower = %+v, want one restart and no kinds: only the leader knows what is declared", follower)
	}
	if leader.Reloads != 1 || !slices.Equal(leader.ResourceKinds, []string{"postgres"}) {
		t.Errorf("leader = %+v, want one restart and [postgres]", leader)
	}
}

func TestASessionCountsOnlyPublishedErrorCodesAndNotTheInterruptOrAnUncodedFailure(t *testing.T) {
	var recorded telemetry.DevSession
	tally := newSession(func(session telemetry.DevSession) { recorded = session })

	tally.noteError(&clierror.Error{Code: "variables.missing", Cause: errors.New("a secret detail")})
	tally.noteError(&clierror.Error{Code: "variables.missing", Cause: errors.New("another")})
	tally.noteError(errors.New("an uncoded failure"))
	tally.noteError(context.Canceled)
	tally.noteError(&exitcode.ExitError{Code: exitcode.Interrupt})
	tally.noteError(nil)
	tally.end(nil)

	if len(recorded.ErrorCodes) != 1 || recorded.ErrorCodes["variables.missing"] != 2 {
		t.Errorf("error codes = %v, want only variables.missing twice", recorded.ErrorCodes)
	}
}

func TestASessionRecordsNothingWhenNoOneListens(t *testing.T) {
	tally := newSession(nil)

	tally.noteReload()
	tally.end(errors.New("anything"))
}
