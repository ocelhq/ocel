package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ocelhq/ocel/platform/realtime/gateway"
)

const (
	hostEnv   = "OCEL_REALTIME_HOST"
	keysEnv   = "OCEL_REALTIME_KEYS"
	listenEnv = "OCEL_REALTIME_LISTEN"

	defaultListen     = ":8080"
	readHeaderTimeout = 10 * time.Second
	readyTimeout      = 2 * time.Second
	stopGrace         = 10 * time.Second
)

type config struct {
	listen string
	host   string
	keys   map[string]ed25519.PublicKey
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
	cfg, err := readConfig(os.Getenv)
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

func readConfig(getenv func(string) string) (config, error) {
	cfg := config{listen: listenAddress(getenv), host: getenv(hostEnv), keys: map[string]ed25519.PublicKey{}}
	if cfg.host == "" {
		return config{}, fmt.Errorf("%s names no host, and every token names the host it was minted for", hostEnv)
	}
	var encoded map[string]string
	if err := json.Unmarshal([]byte(getenv(keysEnv)), &encoded); err != nil {
		return config{}, fmt.Errorf("%s is no JSON object of each namespace's base64 public key: %w", keysEnv, err)
	}
	if len(encoded) == 0 {
		return config{}, fmt.Errorf("%s names no namespace, so the gateway could verify no token", keysEnv)
	}
	for namespace, key := range encoded {
		raw, err := base64.StdEncoding.DecodeString(key)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return config{}, fmt.Errorf("%s names a key for %s that is no base64 Ed25519 public key", keysEnv, namespace)
		}
		cfg.keys[namespace] = raw
	}
	return cfg, nil
}

func serve(ctx context.Context, listener net.Listener, cfg config) error {
	realtime := gateway.New(gateway.Config{
		Host: cfg.host,
		Keys: func(namespace string) (ed25519.PublicKey, bool) {
			key, known := cfg.keys[namespace]
			return key, known
		},
	})
	server := &http.Server{Handler: realtime, ReadHeaderTimeout: readHeaderTimeout}
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
	conn, err := net.DialTimeout("tcp", address, readyTimeout)
	if err != nil {
		return fmt.Errorf("the realtime gateway does not answer on %s: %w", address, err)
	}
	return conn.Close()
}
