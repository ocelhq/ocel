package cli

import (
	"context"
	"io"
	"time"

	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
)

const shutdownSlack = 3 * time.Second

const gracefulShutdownWindow = max(providerrunner.DefaultGracePeriod+providerrunner.DefaultReapTimeout, appChildWaitDelay) + shutdownSlack

func installInterruptHandler(parent context.Context, stderr io.Writer) (context.Context, context.CancelFunc) {
	return exitsig.Install(parent, stderr, gracefulShutdownWindow, forceKillEverything)
}

func forceKillEverything() {
	providerrunner.KillAllLive()
	killAllLiveAppChildren()
}
