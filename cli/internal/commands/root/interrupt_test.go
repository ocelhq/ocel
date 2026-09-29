package root

import (
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/devresources"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
)

func TestAnInterruptedDevRunHasTimeToStopItsContainersBeforeTheHardExit(t *testing.T) {
	if docker.StopsWithin != 6*time.Second {
		t.Errorf("docker.StopsWithin = %s, want 6s: a 3s grace, then docker's kill and the removal", docker.StopsWithin)
	}
	if devresources.StopsWithin < docker.StopsWithin {
		t.Errorf("dev resources are given %s to stop and one container may take %s", devresources.StopsWithin, docker.StopsWithin)
	}
	if spent := childprocess.WaitDelay + devresources.StopsWithin; devShutdownWindow < spent+time.Second {
		t.Errorf("the hard exit lands %s after the interrupt, and the app child then the dev resources may take %s", devShutdownWindow, spent)
	}
	if devShutdownWindow != 14*time.Second {
		t.Errorf("devShutdownWindow = %s, want 14s", devShutdownWindow)
	}
}
