package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/ocelhq/ocel/cloud/aws/server"
	"github.com/ocelhq/ocel/pkg/channel"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ocel aws provider:", err)
		os.Exit(1)
	}
}

func run() error {
	token := os.Getenv(channel.SessionTokenEnvVar)
	if token == "" {
		return fmt.Errorf("%s must be set by the launching CLI", channel.SessionTokenEnvVar)
	}

	ln, addr, err := listen()
	if err != nil {
		return fmt.Errorf("bind provider listener: %w", err)
	}
	defer ln.Close()

	fmt.Fprintf(os.Stderr, "ocel aws provider %s: bound %s\n", version, addr)

	httpSrv := &http.Server{Handler: server.NewMux(token)}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.Serve(ln) }()

	fmt.Println(channel.FormatReadinessLine(addr))

	select {
	case <-ctx.Done():
		return httpSrv.Close()
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	}
}
