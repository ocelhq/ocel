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
	"sync"
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
	return serve(ctx, listener, handler, stopGrace)
}

func serve(ctx context.Context, listener net.Listener, handler http.Handler, grace time.Duration) int {
	var relaying sync.WaitGroup
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			relaying.Add(1)
			defer relaying.Done()
			handler.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		bounded, cancel := context.WithTimeout(context.Background(), grace)
		defer cancel()
		if server.Shutdown(bounded) != nil {
			return
		}
		drained := make(chan struct{})
		go func() {
			relaying.Wait()
			close(drained)
		}()
		select {
		case <-drained:
		case <-bounded.Done():
		}
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	<-stopped
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
