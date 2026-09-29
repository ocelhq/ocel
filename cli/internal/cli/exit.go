package cli

import (
	"context"
	"io"
	"time"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/devstack/docker"
	"github.com/ocelhq/ocel/cli/internal/interrupt"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
)

const shutdownSlack = 3 * time.Second

const gracefulShutdownWindow = max(providerclient.DefaultGracePeriod+providerclient.DefaultReapTimeout, childprocess.WaitDelay) + shutdownSlack

const devStackStopsWithin = docker.StopsWithin

const devShutdownWindow = childprocess.WaitDelay + devStackStopsWithin + shutdownSlack

func installDevInterruptHandler(parent context.Context, stderr io.Writer) (context.Context, context.CancelFunc) {
	return interrupt.Handle(parent, stderr, devShutdownWindow, bus.Interrupt, forceKillEverything)
}

func installInterruptHandler(parent context.Context, stderr io.Writer) (context.Context, context.CancelFunc) {
	return interrupt.Handle(parent, stderr, gracefulShutdownWindow, bus.Interrupt, forceKillEverything)
}

func forceKillEverything() {
	providerclient.KillAllLive()
	childprocess.KillAll()
}
