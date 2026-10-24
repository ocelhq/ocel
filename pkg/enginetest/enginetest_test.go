package enginetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func aRunThatEnded(t *testing.T) string {
	t.Helper()
	gone := exec.Command(os.Args[0], "-test.run=^$")
	if said, err := gone.CombinedOutput(); err != nil {
		t.Fatalf("run a process to its end: %v\n%s", err, said)
	}
	return runOf(hostname(), gone.Process.Pid)
}

func TestARunHasEndedOnlyWhenItsProcessOnThisMachineHas(t *testing.T) {
	t.Parallel()

	for what, want := range map[string]struct {
		of    string
		ended bool
	}{
		"this run":                       {run, false},
		"a run whose process still runs": {runOf(hostname(), os.Getppid()), false},
		"a run whose process is gone":    {aRunThatEnded(t), true},
		"a run on another machine":       {runOf(hostname()+"-elsewhere", 1<<22), false},
		"a label nothing here wrote":     {"unreadable", false},
		"a pid that names no process":    {runOf(hostname(), 0), false},
	} {
		if got := runProcessGone(want.of); got != want.ended {
			t.Errorf("%s (%q): ended = %v, want %v: a live run swept loses what it depends on mid-test, and a dead one never swept leaves its containers and networks on the machine for good", what, want.of, got, want.ended)
		}
	}
}

func TestABucketOnTheSharedStoreIsTakenByOneTestAlone(t *testing.T) {
	t.Parallel()

	keys := taken{by: map[string]string{}}
	if err := keys.take("uploads", "TestFirst"); err != nil {
		t.Fatalf("the first take of a bucket = %v", err)
	}
	err := keys.take("uploads", "TestSecond")
	if err == nil || !strings.Contains(err.Error(), "TestFirst") {
		t.Errorf("a second test taking the same bucket was told %v, want a refusal naming the test that took it", err)
	}
	if err := keys.take("uploads", "TestFirst"); err != nil {
		t.Errorf("the test that took a bucket was refused it again: %v", err)
	}
	if err := keys.take("downloads", "TestSecond"); err != nil {
		t.Errorf("a bucket nothing took was refused: %v", err)
	}
}

func TestARootAnotherRunAlreadySweptIsReclaimedWithoutComplaint(t *testing.T) {
	t.Parallel()

	gone := filepath.Join(t.TempDir(), rootPrefix+"swept")
	if err := removeRunRoot(gone); err != nil {
		t.Errorf("reclaimed(%s) = %v, want nil: two runs that sweep at once both list a root the first one then removes, and the second is left failing its whole run over a directory nobody owns any more", gone, err)
	}
}

const withoutDocker = "OCEL_ENGINETEST_WITHOUT_DOCKER"

func TestAChildAsksForDockerOnAMachineWithoutIt(t *testing.T) {
	if os.Getenv(withoutDocker) == "" {
		t.Skip("run only as the child of the test that hides docker from it")
	}
	RequireDocker(t)
}

func TestATestAskingForDockerOnAMachineWithoutItFailsRatherThanSkips(t *testing.T) {
	t.Parallel()

	child := exec.Command(os.Args[0], "-test.run=^TestAChildAsksForDockerOnAMachineWithoutIt$", "-test.v")
	child.Env = append(os.Environ(), withoutDocker+"=1", "PATH="+t.TempDir())
	said, err := child.CombinedOutput()
	if err == nil || !strings.Contains(string(said), "--- FAIL") {
		t.Errorf("a test asking for docker where there is none ended with err = %v:\n%s\nwant it failed: a suite that skips there passes green with nothing exercised", err, said)
	}
}
