// Command aws is the Ocel AWS provider binary. It speaks the T2 provider
// protocol (pkg/proto/provider/v1): it binds a private local channel,
// prints the readiness sentinel once bound, verifies the per-session token
// on every call, and serves a stubbed ProviderService.Deploy. Real
// provisioning against AWS lands here later, pulling the AWS SDK into THIS
// module only.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
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
	token := os.Getenv(providerv1.SessionTokenEnvVar)
	if token == "" {
		return fmt.Errorf("%s must be set by the launching CLI", providerv1.SessionTokenEnvVar)
	}

	ln, addr, err := listen()
	if err != nil {
		return fmt.Errorf("bind provider listener: %w", err)
	}
	defer ln.Close()

	fmt.Fprintf(os.Stderr, "ocel aws provider %s: bound %s\n", version, addr)

	httpSrv := &http.Server{Handler: newMux(token)}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.Serve(ln) }()

	// The listener above is already bound; print the readiness sentinel now
	// so the CLI can dial in. Any other stdout/stderr, before or after this
	// line, is diagnostic log output, not protocol.
	fmt.Println(providerv1.FormatReadinessLine(addr))

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
