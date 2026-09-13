package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ocelhq/ocel/pkg/costkit"
	"github.com/ocelhq/ocel/pricing"
	"github.com/ocelhq/ocel/pricing/store/postgres"
)

const (
	defaultPort  = "8090"
	readTimeout  = 30 * time.Second
	writeTimeout = 60 * time.Second
	shutdownWait = 15 * time.Second
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("the pricer stopped", "error", err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	card, err := pricing.Cards()
	if err != nil {
		return err
	}
	var store costkit.Store = card
	if url := os.Getenv("DATABASE_URL"); url != "" {
		held, err := postgres.Open(ctx, url, card)
		if err != nil {
			return err
		}
		defer held.Close()
		store = held
	} else {
		log.Info("DATABASE_URL names no rate store, so prices come from the embedded cards alone")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}
	srv := &http.Server{
		Addr:              net.JoinHostPort("", port),
		Handler:           pricing.Handler(store, pricing.Options{Tokens: pricing.Tokens(os.Getenv(pricing.TokensVariable)), Logger: log}),
		ReadHeaderTimeout: readTimeout,
		WriteTimeout:      writeTimeout,
	}

	served := make(chan error, 1)
	go func() { served <- srv.ListenAndServe() }()
	log.Info("the pricer is listening", "port", port)

	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		stopping, cancel := context.WithTimeout(context.Background(), shutdownWait)
		defer cancel()
		return srv.Shutdown(stopping)
	}
}
