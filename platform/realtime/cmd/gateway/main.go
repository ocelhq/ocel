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

	"github.com/ocelhq/ocel/platform/realtime/gateway"
	"github.com/ocelhq/ocel/platform/realtime/gatewayenv"
)

const (
	listenEnv = "OCEL_REALTIME_LISTEN"

	defaultListen     = ":" + gatewayenv.ListenPort
	readyPath         = "/ready"
	readHeaderTimeout = 10 * time.Second
	readyTimeout      = 2 * time.Second
	stopGrace         = 10 * time.Second
)

type config struct {
	listen          string
	host            string
	keys            *keySet
	allowsAnyOrigin bool
	maxSockets      int
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 && args[0] == "ready" {
		if err := probeReady(readyAddress(listenAddress(os.Getenv))); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	cfg, err := readConfig(os.Getenv, time.Now)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	listener, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen on %s: %v\n", cfg.listen, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := serve(ctx, listener, cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func listenAddress(getenv func(string) string) string {
	if listen := getenv(listenEnv); listen != "" {
		return listen
	}
	return defaultListen
}

func readConfig(getenv func(string) string, now func() time.Time) (config, error) {
	cfg := config{listen: listenAddress(getenv), host: getenv(gatewayenv.HostVar)}
	if cfg.host == "" {
		return config{}, fmt.Errorf("%s names no host, and every token names the host it was minted for", gatewayenv.HostVar)
	}
	keys, err := readKeySet(getenv, now)
	if err != nil {
		return config{}, err
	}
	cfg.keys = keys
	if raw := getenv(gatewayenv.AnyOriginVar); raw != "" {
		if cfg.allowsAnyOrigin, err = strconv.ParseBool(raw); err != nil {
			return config{}, fmt.Errorf("%s is %q, not true or false", gatewayenv.AnyOriginVar, raw)
		}
	}
	if raw := getenv(gatewayenv.MaxSocketsVar); raw != "" {
		if cfg.maxSockets, err = strconv.Atoi(raw); err != nil || cfg.maxSockets < 1 {
			return config{}, fmt.Errorf("%s is %q, not a count of sockets above 0", gatewayenv.MaxSocketsVar, raw)
		}
	}
	return cfg, nil
}

func readKeySet(getenv func(string) string, now func() time.Time) (*keySet, error) {
	encoded, path := getenv(gatewayenv.KeysVar), getenv(gatewayenv.KeysFileVar)
	switch {
	case encoded != "" && path != "":
		return nil, fmt.Errorf("%s and %s both name keys; set one", gatewayenv.KeysVar, gatewayenv.KeysFileVar)
	case path != "":
		return newKeysFileSet(path, now)
	default:
		return newFixedKeySet(gatewayenv.KeysVar, encoded)
	}
}

func serve(ctx context.Context, listener net.Listener, cfg config) error {
	realtime := gateway.New(gateway.Config{
		Host:            cfg.host,
		Keys:            cfg.keys.find,
		AllowsAnyOrigin: cfg.allowsAnyOrigin,
		MaxSockets:      cfg.maxSockets,
	})
	answering := http.NewServeMux()
	answering.Handle("/", realtime)
	answering.HandleFunc("GET "+readyPath, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	server := &http.Server{Handler: answering, ReadHeaderTimeout: readHeaderTimeout}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()

	var failed error
	select {
	case <-ctx.Done():
	case failed = <-served:
	}
	grace, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopGrace)
	defer cancel()
	stopped := errors.Join(server.Shutdown(grace), realtime.Close(grace))
	if failed != nil && !errors.Is(failed, http.ErrServerClosed) {
		return errors.Join(failed, stopped)
	}
	return stopped
}

func readyAddress(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil || host == "" {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return listen
}

func probeReady(address string) error {
	client := &http.Client{Timeout: readyTimeout}
	answer, err := client.Get("http://" + address + readyPath)
	if err != nil {
		return fmt.Errorf("the realtime gateway does not answer on %s: %w", address, err)
	}
	_ = answer.Body.Close()
	if answer.StatusCode != http.StatusNoContent {
		return fmt.Errorf("what answers on %s is no realtime gateway: %s answered %s", address, readyPath, answer.Status)
	}
	return nil
}
