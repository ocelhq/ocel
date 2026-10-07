package root

import (
	"context"
	"io"
	"time"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
	"github.com/ocelhq/ocel/cli/internal/interrupt"
	"github.com/ocelhq/ocel/cli/internal/livedir"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
)

const shutdownSlack = 3 * time.Second

const gracefulShutdownWindow = max(providerprocess.DefaultGracePeriod+providerprocess.DefaultReapTimeout, childprocess.WaitDelay) + shutdownSlack

const devShutdownWindow = childprocess.WaitDelay + docker.StopsWithin + shutdownSlack

func installDevInterruptHandler(parent context.Context, stderr io.Writer, bus *run.Bus) (context.Context, context.CancelFunc) {
	return interrupt.Handle(parent, stderr, devShutdownWindow, bus.Interrupt, killChildrenAndRemoveLiveDirs)
}

func installInterruptHandler(parent context.Context, stderr io.Writer, bus *run.Bus) (context.Context, context.CancelFunc) {
	return interrupt.Handle(parent, stderr, gracefulShutdownWindow, bus.Interrupt, killChildrenAndRemoveLiveDirs)
}

func killChildrenAndRemoveLiveDirs() {
	providerprocess.KillAllLive()
	childprocess.KillAll()
	livedir.RemoveRecorded()
}
