// Command ocelaws is the Ocel AWS provider binary. It speaks the provider
// protocol (pkg/proto/deployments/v1): it binds a private local channel, prints
// the readiness sentinel once bound, verifies the per-session token on every
// call, and serves DeploymentService (Deploy + Bootstrap). The provisioning
// logic lives in the sibling deploy/bootstrap/server packages; this
// entrypoint only wires transport.
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

// version is set at build time via -ldflags "-X main.version=...".
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

	// The listener above is already bound; print the readiness sentinel now
	// so the CLI can dial in. Any other stdout/stderr, before or after this
	// line, is diagnostic log output, not protocol.
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
