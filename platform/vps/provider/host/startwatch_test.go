package host

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const crashLooping = "Status=restarting ExitCode=1 OOMKilled=false Error= StartedAt=2026-01-01T00:00:09Z FinishedAt=2026-01-01T00:00:09Z RestartCount=10"

func searchedOnCrashing(found session.Result, state string) *bench {
	box := searchedOnIdling(found, true)
	searched := box.answer
	box.answer = func(command string) (session.Result, bool) {
		switch {
		case command == stateCommand(physical):
			return session.Result{Stdout: state}, true
		case command == renderLastRunLogs(physical):
			return session.Result{Stdout: "Error: Cannot find module '/app/index.js'\nocel: the app exited: exit status 1\n"}, true
		case strings.HasPrefix(command, "docker logs"):
			return session.Result{Stdout: "2026-01-01T00:00:01Z first run\n2026-01-01T00:00:09Z last run\n"}, true
		}
		return searched(command)
	}
	return box
}

func insideWatch(command string) string {
	written := quoted(command)
	return written[1 : len(written)-1]
}

func TestAnAppWhoseContainerExitsBeforeItAnswersIsRefusedAsFailingToStartAndNeverAsAHealthCheck(t *testing.T) {
	t.Parallel()

	for what, found := range map[string]session.Result{
		"the watcher stopped the probe": {Code: 1, Stdout: unstartedMarker + " " + physical + "\n"},
		"the probe ran out its window":  {Code: 4, Stderr: nextTarget + " never answered /up within 1m0s"},
	} {
		box := searchedOnCrashing(found, crashLooping)
		_, err := box.host().FindHealthPath(context.Background(), aHealthPathSearch(), nil)
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
			t.Fatalf("%s: FindHealthPath() = %v, want a not-ready refusal", what, err)
		}
		said := err.Error()
		for _, want := range []string{"start node on", "failing to start, not failing a health check", "RestartCount=10", "logs (its last run, last " + appLogTail + " lines):\nError: Cannot find module '/app/index.js'", physical + " removed"} {
			if !strings.Contains(said, want) {
				t.Errorf("%s: the refusal reads\n%s\nand never says %q", what, said, want)
			}
		}
		for _, misleading := range []string{healthKey, "never answered", "first run"} {
			if strings.Contains(said, misleading) {
				t.Errorf("%s: the refusal reads\n%s\nand says %q, which points at a health check or at a run before the last", what, said, misleading)
			}
		}
	}
}

func TestAnAppStillRunningWhenTheProbeGivesUpIsRefusedAsNeverAnswering(t *testing.T) {
	t.Parallel()

	box := searchedOnCrashing(session.Result{Code: 4, Stderr: nextTarget + " never answered /up within 1m0s"},
		"Status=running ExitCode=0 OOMKilled=false Error= StartedAt=2026-01-01T00:00:00Z FinishedAt=0001-01-01T00:00:00Z RestartCount=0")
	_, err := box.host().FindHealthPath(context.Background(), aHealthPathSearch(), nil)
	if err == nil || !strings.Contains(err.Error(), "never answered /up") || strings.Contains(err.Error(), "failing to start") {
		t.Errorf("FindHealthPath() = %v, want a running app that never answered refused as never answering", err)
	}
}

func TestAGateWhoseContainerExitsBeforeItAnswersIsRefusedAsFailingToStart(t *testing.T) {
	t.Parallel()

	for what, gate := range map[string]session.Result{
		"the watcher stopped the gate": {Code: 1, Stdout: unstartedMarker + " " + physical + "\n"},
		"the gate ran out its window":  {Code: 4, Stderr: nextTarget + " never answered /healthz within 30s"},
	} {
		said := diagnosed(t, gate, crashLooping, "Error: Cannot find module '/app/index.js'\n")
		for _, want := range []string{"the previous release is still live", "failing to start, not failing a health check", "Status=restarting", "Cannot find module"} {
			if !strings.Contains(said, want) {
				t.Errorf("%s: the refusal reads\n%s\nand never says %q", what, said, want)
			}
		}
	}
}

func TestTheGateWatchesEveryContainerItReleases(t *testing.T) {
	t.Parallel()

	box, err := rolledOut(t, bothApps(), session.Result{}, session.Result{}, nil)
	if err != nil {
		t.Fatalf("Release() = %v", err)
	}
	gated := box.commands()[box.at(quoted("gate"))]
	for _, name := range []string{containerOf(nextTarget), containerOf(apiNextTarget)} {
		if !strings.Contains(gated, insideWatch("crashed "+quoted(name))) {
			t.Errorf("the gate ran\n%s\nand never watches %s", gated, name)
		}
	}
}

func TestTheLastRunsLogsAreReadFromWhenItStartedAndNoMoreThanTheTail(t *testing.T) {
	t.Parallel()

	command := renderLastRunLogs(physical)
	for _, want := range []string{`--since "$started"`, "--tail " + appLogTail} {
		if !strings.Contains(command, want) {
			t.Errorf("the last run's logs are read with\n%s\nwhich has no %s: an app that logs fast before it dies would hand back every line it wrote", command, want)
		}
	}
}
