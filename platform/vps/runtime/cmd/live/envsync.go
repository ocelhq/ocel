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

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/envsource"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	"github.com/ocelhq/ocel/platform/vps/provider/helpers"
)

const (
	envSyncCommand = "envsync"
	requestWindow  = 30 * time.Second
)

func envSync(argv []string, errs io.Writer) int {
	flags := flag.NewFlagSet("ocel-live "+envSyncCommand, flag.ContinueOnError)
	flags.SetOutput(errs)
	class := flags.String("class", "", "the class whose values this syncer keeps in step with their env sources")
	if err := flags.Parse(argv); err != nil {
		return 2
	}
	switch providerkit.Class(*class) {
	case providerkit.ClassProduction, providerkit.ClassPreview:
	default:
		fmt.Fprintf(errs, "ocel-live %s: --class is %q, want %s or %s\n", envSyncCommand, *class, providerkit.ClassProduction, providerkit.ClassPreview)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	local := helpers.Local{}
	syncer := &envsource.Syncer{
		Store:  values.Store{Records: helpers.RecordsOver(local), Sealer: helpers.SealerOver(local)},
		Class:  providerkit.Class(*class),
		Target: envsource.Target{Client: &http.Client{Timeout: requestWindow}},
	}
	syncer.Run(ctx, func(err error) { fmt.Fprintf(errs, "ocel-live %s: %s\n", envSyncCommand, err) })
	return 0
}
