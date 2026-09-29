package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

const envSourceRequestTimeout = 30 * time.Second

func envSourceSync(argv []string, errs io.Writer) int {
	named := "ocel-live " + live.EnvSourceSyncCommand
	flags := flag.NewFlagSet(named, flag.ContinueOnError)
	flags.SetOutput(errs)
	tier := flags.String("tier", "", "the tier whose values this sync keeps in step with their env sources")
	if err := flags.Parse(argv); err != nil {
		return 2
	}
	switch environment.Tier(*tier) {
	case environment.TierProduction, environment.TierPreview:
	default:
		fmt.Fprintf(errs, "%s: --tier is %q, want %s or %s\n", named, *tier, environment.TierProduction, environment.TierPreview)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	local := boxstore.LocalTransport{}
	sync := &envsource.Sync{
		Store: variablestore.Store{KeyValues: boxstore.NewKeyValues(local), Cipher: boxstore.NewCipher(local)},
		Tier:  environment.Tier(*tier),
		Login: envsource.Login{Client: &http.Client{Timeout: envSourceRequestTimeout}},
	}
	sync.CopyScheduledEveryInterval(ctx, func(err error) { fmt.Fprintf(errs, "%s: %s\n", named, err) })
	return 0
}
