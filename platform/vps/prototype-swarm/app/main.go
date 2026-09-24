package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "probe" {
		resp, err := (&http.Client{Timeout: 2 * time.Second}).Get("http://127.0.0.1:8080/health")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
	}
	version := os.Getenv("VERSION")
	host, _ := os.Hostname()
	node := os.Getenv("NODE")
	readyAfter, _ := strconv.Atoi(os.Getenv("READY_AFTER"))
	started := time.Now()
	var stopping atomic.Bool

	secret := func() string {
		b, err := os.ReadFile("/run/secrets/app_secret")
		if err != nil {
			return "-"
		}
		return strings.TrimSpace(string(b))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if os.Getenv("BROKEN") == "1" || stopping.Load() || time.Since(started) < time.Duration(readyAfter)*time.Second {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if d, err := time.ParseDuration(r.URL.Query().Get("sleep")); err == nil {
			time.Sleep(d)
		}
		fmt.Fprintf(w, "version=%s task=%s node=%s secret=%s client=%s xff=%s\n", version, host, node, secret(), r.RemoteAddr, r.Header.Get("X-Forwarded-For"))
	})

	server := &http.Server{Addr: ":8080", Handler: mux}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		stopping.Store(true)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
