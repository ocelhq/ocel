package root

import (
	"errors"
	"os"
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

func TestAHardExitLeavesNoLiveDirOnDisk(t *testing.T) {
	dir, err := livedir.Write(t.TempDir(), "ocel-live-", map[string]string{"SESSION_SECRET": "ss_live"})
	if err != nil {
		t.Fatal(err)
	}

	killChildrenAndRemoveLiveDirs()

	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat %s after the hard exit's teardown: err = %v, want it removed", dir, err)
	}
}
