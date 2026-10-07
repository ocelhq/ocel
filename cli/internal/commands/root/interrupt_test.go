package root

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
	"github.com/ocelhq/ocel/cli/internal/livedir"
)

func TestAnInterruptedDevRunHasTimeToStopItsContainersBeforeTheHardExit(t *testing.T) {
	if docker.StopsWithin != 6*time.Second {
		t.Errorf("docker.StopsWithin = %s, want 6s: a 3s grace, then docker's kill and the removal", docker.StopsWithin)
	}
	if spent := childprocess.WaitDelay + docker.StopsWithin; devShutdownWindow < spent+time.Second {
		t.Errorf("the hard exit lands %s after the interrupt, and the app child then the dev resources may take %s", devShutdownWindow, spent)
	}
	if devShutdownWindow != 14*time.Second {
		t.Errorf("devShutdownWindow = %s, want 14s", devShutdownWindow)
	}
}

const hardExitParentEnvVar = "OCEL_TEST_HARD_EXIT_PARENT"

func TestAHardExitLeavesNoLiveDirOnDiskAndCreatesNoneAfter(t *testing.T) {
	parent := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), hardExitParentEnvVar+"="+parent)

	if said, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the hard exit's teardown: %v\n%s", err, said)
	}

	if entries, _ := os.ReadDir(parent); len(entries) > 0 {
		t.Errorf("%d entries are left under %s after the hard exit's teardown, want every live dir removed and none created after", len(entries), parent)
	}
}

func runHardExitTeardown(parent string) int {
	if _, err := livedir.Write(parent, "ocel-live-", map[string]string{"SESSION_SECRET": "ss_live"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	killChildrenAndRemoveLiveDirs()

	if _, err := livedir.Write(parent, "ocel-live-", map[string]string{"SESSION_SECRET": "ss_live"}); err == nil {
		fmt.Fprintln(os.Stderr, "a write after the teardown created a live dir")
		return 1
	}
	return 0
}
