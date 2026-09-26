package cli

import (
	"context"
	"io"
	"time"

	"github.com/ocelhq/ocel/cli/internal/devstack/docker"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/runui"
)

const shutdownSlack = 3 * time.Second

const gracefulShutdownWindow = max(providerclient.DefaultGracePeriod+providerclient.DefaultReapTimeout, appChildWaitDelay) + shutdownSlack

const devStackStopsWithin = docker.StopsWithin

const devShutdownWindow = appChildWaitDelay + devStackStopsWithin + shutdownSlack

func installDevInterruptHandler(parent context.Context, stderr io.Writer) (context.Context, context.CancelFunc) {
	return exitsig.Install(parent, stderr, devShutdownWindow, runui.Interrupt, forceKillEverything)
}

func installInterruptHandler(parent context.Context, stderr io.Writer) (context.Context, context.CancelFunc) {
	return exitsig.Install(parent, stderr, gracefulShutdownWindow, runui.Interrupt, forceKillEverything)
}

func forceKillEverything() {
	providerclient.KillAllLive()
	killAllLiveAppChildren()
}
