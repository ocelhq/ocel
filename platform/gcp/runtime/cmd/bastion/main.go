package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/platform/gcp/provider/relay"
)

const (
	portEnv           = "PORT"
	readHeaderTimeout = 10 * time.Second
	stopGrace         = 10 * time.Second
)

func main() {
	os.Exit(run(os.Getenv))
}

func run(getenv func(string) string) int {
	handler, err := newHandler(getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	listener, err := net.Listen("tcp", listenAddress(getenv))
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen on %s: %v\n", listenAddress(getenv), err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: readHeaderTimeout}
	go func() {
		<-ctx.Done()
		grace, cancel := context.WithTimeout(context.Background(), stopGrace)
		defer cancel()
		_ = server.Shutdown(grace)
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func listenAddress(getenv func(string) string) string {
	if port := getenv(portEnv); port != "" {
		return ":" + port
	}
	return ":" + strconv.Itoa(containerimage.Port)
}

func newHandler(getenv func(string) string) (http.Handler, error) {
	allowed, err := relay.ParseDestinations(getenv(relay.AllowedEnv))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", relay.AllowedEnv, err)
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("%s names no destination, and a bastion forwards to the destinations it names alone", relay.AllowedEnv)
	}
	return relay.NewHandler(allowed), nil
}
