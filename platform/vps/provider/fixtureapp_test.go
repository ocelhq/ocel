package vps_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const fixtureSource = `package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var accepted []net.Conn

var inflight atomic.Int64

func main() {
	if os.Getenv("MODE") == "crash" {
		os.Exit(3)
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		panic(err)
	}
	if os.Getenv("MODE") == "hang" {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted = append(accepted, conn)
		}
	}
	release := os.Getenv("RELEASE")
	health, err := strconv.Atoi(os.Getenv("HEALTH_STATUS"))
	if err != nil {
		health = http.StatusOK
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(health)
		_, _ = io.WriteString(w, release)
	})
	mux.HandleFunc("/env", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		for _, live := range strings.Split(os.Getenv("OCEL_LIVE_KEYS"), ",") {
			if live != name {
				continue
			}
			read, err := os.ReadFile(filepath.Join(os.Getenv("OCEL_LIVE_DIR"), name))
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			_, _ = w.Write(read)
			return
		}
		_, _ = io.WriteString(w, os.Getenv(name))
	})
	mux.HandleFunc("/inflight", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strconv.FormatInt(inflight.Load(), 10))
	})
	mux.HandleFunc("/hold", func(w http.ResponseWriter, r *http.Request) {
		inflight.Add(1)
		defer inflight.Add(-1)
		seconds, _ := strconv.Atoi(r.URL.Query().Get("s"))
		time.Sleep(time.Duration(seconds) * time.Second)
		_, _ = io.WriteString(w, release)
	})
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		inflight.Add(1)
		defer inflight.Add(-1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", release)
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, _ *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			return
		}
		inflight.Add(1)
		defer inflight.Add(-1)
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		for {
			_, _ = io.WriteString(conn, "\x00tick\xff")
			time.Sleep(time.Second)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, release)
	})
	panic(http.Serve(listener, mux))
}
`

func fixtureBuilt(t *testing.T, goos, arch string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(fixtureSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module ocelfixture\n\ngo 1.25\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	built := filepath.Join(dir, "app")
	build := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", built, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+arch, "GOFLAGS=")
	if said, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the fixture app: %v\n%s", err, said)
	}
	return built
}

func TestTheHungFixtureStaysUpAndAnswersNothingItAccepts(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	run := exec.Command(fixtureBuilt(t, runtime.GOOS, runtime.GOARCH))
	run.Env = []string{"MODE=hang", "PORT=" + port}
	wrote, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	run.Stdout, run.Stderr = wrote, wrote
	said := func() string {
		read, err := os.ReadFile(wrote.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(read)
	}
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	died := make(chan error, 1)
	go func() { died <- run.Wait() }()
	t.Cleanup(func() {
		_ = run.Process.Kill()
		<-died
	})

	bound := false
	for range 100 {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second)
		if err == nil {
			_ = conn.Close()
			bound = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !bound {
		t.Fatalf("the hung fixture never bound %s, and connection-refused is a different bug from never-answered:\n%s", port, said())
	}

	client := &http.Client{Timeout: 2 * time.Second}
	answer, err := client.Get("http://127.0.0.1:" + port + healthPath)
	if err == nil {
		_ = answer.Body.Close()
		t.Fatalf("the hung fixture answered %s with %d, and the gate it is built to defeat would pass it", healthPath, answer.StatusCode)
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "Client.Timeout") {
		t.Fatalf("a probe of the hung fixture failed with %v, want a connection accepted and left unanswered", err)
	}
	select {
	case fell := <-died:
		t.Fatalf("the hung fixture ended with %v, so a restart policy loops it and a crash is what the release reports:\n%s", fell, said())
	default:
	}
	if written := said(); written != "" {
		t.Errorf("the hung fixture wrote\n%s\nand a refusal quoting its logs would no longer read as the absence the diagnosis turns on", written)
	}
}

const healthPath = "/healthz"
