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

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envvars"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

const envSourceRequestTimeout = 30 * time.Second

func envSourceSync(argv []string, errs io.Writer) int {
	named := "ocel-live " + live.EnvSourceSyncCommand
	flags := flag.NewFlagSet(named, flag.ContinueOnError)
	flags.SetOutput(errs)
	class := flags.String("class", "", "the class whose values this sync keeps in step with their env sources")
	if err := flags.Parse(argv); err != nil {
		return 2
	}
	switch edge.Class(*class) {
	case edge.ClassProduction, edge.ClassPreview:
	default:
		fmt.Fprintf(errs, "%s: --class is %q, want %s or %s\n", named, *class, edge.ClassProduction, edge.ClassPreview)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	local := boxstore.LocalTransport{}
	sync := &envsource.Sync{
		Store: envvars.Store{Records: boxstore.NewRecords(local), Cipher: boxstore.NewCipher(local)},
		Class: edge.Class(*class),
		Login: envsource.Login{Client: &http.Client{Timeout: envSourceRequestTimeout}},
	}
	sync.CopyScheduledEveryInterval(ctx, func(err error) { fmt.Fprintf(errs, "%s: %s\n", named, err) })
	return 0
}
