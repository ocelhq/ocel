package root

import (
	"context"
	"io"
	"time"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/devresources"
	"github.com/ocelhq/ocel/cli/internal/interrupt"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
)

const shutdownSlack = 3 * time.Second

const gracefulShutdownWindow = max(providerprocess.DefaultGracePeriod+providerprocess.DefaultReapTimeout, childprocess.WaitDelay) + shutdownSlack

const devShutdownWindow = childprocess.WaitDelay + devresources.StopsWithin + shutdownSlack

func installDevInterruptHandler(parent context.Context, stderr io.Writer) (context.Context, context.CancelFunc) {
	return interrupt.Handle(parent, stderr, devShutdownWindow, bus.Interrupt, forceKillEverything)
}

func installInterruptHandler(parent context.Context, stderr io.Writer) (context.Context, context.CancelFunc) {
	return interrupt.Handle(parent, stderr, gracefulShutdownWindow, bus.Interrupt, forceKillEverything)
}

func forceKillEverything() {
	providerprocess.KillAllLive()
	childprocess.KillAll()
}
