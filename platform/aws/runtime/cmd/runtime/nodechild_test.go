package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func controlPair(t *testing.T) (net.Listener, func() net.Conn) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln, func() net.Conn {
		c, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
}

func TestAwaitReady(t *testing.T) {
	t.Run("returns port on server ready", func(t *testing.T) {
		ln, dial := controlPair(t)
		go func() {
			c := dial()
			fmt.Fprintln(c, `{"type":"log","payload":{"message":"booting"}}`)
			fmt.Fprintln(c, `{"type":"server-ready","payload":{"httpPort":41234}}`)
		}()

		r, shook := awaitReady(watchReady(ln, make(chan error, 1), nil, nil), time.Second)
		if !shook {
			t.Fatal("awaitReady() reported nothing yet, want the handshake it was handed")
		}
		if r.err != nil {
			t.Fatalf("awaitReady() error = %v, want nil", r.err)
		}
		if r.ready.httpPort != 41234 {
			t.Errorf("httpPort = %d, want 41234", r.ready.httpPort)
		}
	})

	t.Run("aborts immediately when node exits before connecting", func(t *testing.T) {
		ln, _ := controlPair(t)
		exited := make(chan error, 1)
		exited <- errors.New("exit status 1")

		start := time.Now()
		r, _ := awaitReady(watchReady(ln, exited, nil, nil), 30*time.Second)
		if r.err == nil {
			t.Fatal("awaitReady() error = nil, want an error")
		}
		if !strings.Contains(r.err.Error(), "exit status 1") {
			t.Errorf("error = %q, want it to carry the child's exit status", r.err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("took %s, want an immediate abort rather than waiting out the budget", elapsed)
		}
	})

	t.Run("reaps a real child that dies without connecting", func(t *testing.T) {
		ln, _ := controlPair(t)
		cmd := exec.Command("sh", "-c", "exit 1")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()

		start := time.Now()
		r, _ := awaitReady(watchReady(ln, exited, nil, nil), 30*time.Second)
		if r.err == nil {
			t.Fatal("awaitReady() error = nil, want the child's exit reported")
		}
		if !strings.Contains(r.err.Error(), "exit status 1") {
			t.Errorf("error = %q, want it to carry the real child's exit status", r.err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("took %s, want the reaper to abort as soon as the child died", elapsed)
		}
	})

	t.Run("hands back nothing yet when node has not signalled ready within the budget", func(t *testing.T) {
		ln, dial := controlPair(t)
		go func() {
			c := dial()
			fmt.Fprintln(c, `{"type":"log","payload":{"message":"still working"}}`)
			select {}
		}()

		r, shook := awaitReady(watchReady(ln, make(chan error, 1), nil, nil), 100*time.Millisecond)
		if shook {
			t.Errorf("awaitReady() = %+v, true, want nothing yet from a node that has not signalled", r)
		}
	})

	t.Run("carries the last log into the error", func(t *testing.T) {
		ln, dial := controlPair(t)
		exited := make(chan error, 1)
		go func() {
			c := dial()
			fmt.Fprintln(c, `{"type":"log","payload":{"message":"SyntaxError: unexpected token"}}`)
			time.Sleep(50 * time.Millisecond)
			exited <- errors.New("exit status 1")
		}()

		r, _ := awaitReady(watchReady(ln, exited, nil, nil), 5*time.Second)
		if r.err == nil {
			t.Fatal("awaitReady() error = nil, want an error")
		}
		if !strings.Contains(r.err.Error(), "SyntaxError: unexpected token") {
			t.Errorf("error = %q, want it to carry the last log node reported", r.err)
		}
	})
}

func TestEntrypointPath(t *testing.T) {
	const nodeEntry = "/opt/ocel/node/entrypoint.mjs"
	const nextEntry = "/opt/ocel/next/entrypoint.mjs"

	cases := []struct {
		name   string
		config string
		want   string
	}{
		{"next runtime", `{"runtime":{"name":"next"}}`, nextEntry},
		{"node runtime", `{"runtime":{"name":"node"}}`, nodeEntry},
		{"empty runtime", `{"runtime":{"name":""}}`, nodeEntry},
		{"no config file", "", nodeEntry},
		{"invalid json", `{not json`, nodeEntry},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.config != "" {
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tc.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("LAMBDA_TASK_ROOT", dir)
			if got := entrypointPath(readArtifact()); got != tc.want {
				t.Errorf("entrypointPath() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBoundReadiness(t *testing.T) {
	t.Run("an invocation that carries no deadline still bounds the wait", func(t *testing.T) {
		ctx, cancel := boundReadiness(t.Context())
		defer cancel()

		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("the readiness wait has no deadline, want it bounded rather than waiting for an app that may never come up")
		}
		if remaining := time.Until(deadline); remaining <= 0 || remaining > maxInvocationBudget {
			t.Errorf("bound = %s, want a positive bound no longer than %s", remaining, maxInvocationBudget)
		}
	})

	t.Run("the invocation's own deadline is what it waits to", func(t *testing.T) {
		want := time.Now().Add(3 * time.Second)
		invocation, cancelInvocation := context.WithDeadline(t.Context(), want)
		defer cancelInvocation()

		ctx, cancel := boundReadiness(invocation)
		defer cancel()

		got, ok := ctx.Deadline()
		if !ok || !got.Equal(want) {
			t.Errorf("deadline = %s, %v, want the invocation's own %s", got, ok, want)
		}
	})

	t.Run("a deadline-less invocation waits for a node that signals late", func(t *testing.T) {
		app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "served without a deadline")
		}))
		defer app.Close()

		ln, dial := controlPair(t)
		exited := make(chan error, 1)
		go func() {
			time.Sleep(300 * time.Millisecond)
			fmt.Fprintf(dial(), "{\"type\":\"server-ready\",\"payload\":{\"httpPort\":%d}}\n", portOf(t, app))
		}()

		m, err := serveWhenReady(watchReady(ln, exited, nil, nil), ln, exited, 50*time.Millisecond)
		if err != nil {
			t.Fatalf("serveWhenReady: %v", err)
		}

		rt, captured := fakeRuntime(t, []byte(getEvent))
		if err := handleInvocation(t.Context(), rt, m); err != nil {
			t.Fatalf("handleInvocation: %v", err)
		}
		_, body := splitPrelude(t, captured.body)
		if want := "served without a deadline"; string(body) != want {
			t.Errorf("body = %q, want %q — the bound ends a hang, it does not cut the wait short", body, want)
		}
	})
}

func TestServeWhenReady(t *testing.T) {
	t.Run("an invocation waits for a late handshake and forwards to the port it names", func(t *testing.T) {
		app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "served after the budget")
		}))
		defer app.Close()

		ln, dial := controlPair(t)
		exited := make(chan error, 1)
		go func() {
			time.Sleep(300 * time.Millisecond)
			fmt.Fprintf(dial(), "{\"type\":\"server-ready\",\"payload\":{\"httpPort\":%d}}\n", portOf(t, app))
		}()

		m, err := serveWhenReady(watchReady(ln, exited, nil, nil), ln, exited, 50*time.Millisecond)
		if err != nil {
			t.Fatalf("serveWhenReady: %v, want a node served late rather than never", err)
		}

		rt, captured := fakeRuntimeWithDeadline(t, []byte(getEvent), time.Now().Add(20*time.Second))
		if err := handleInvocation(t.Context(), rt, m); err != nil {
			t.Fatalf("handleInvocation: %v", err)
		}
		_, body := splitPrelude(t, captured.body)
		if want := "served after the budget"; string(body) != want {
			t.Errorf("body = %q, want %q — the invocation waited for the handshake rather than being refused", body, want)
		}
	})

	t.Run("a node that never signals fails the invocation and leaves the loop running", func(t *testing.T) {
		ln, err := net.Listen("unix", filepath.Join(t.TempDir(), "control.sock"))
		if err != nil {
			t.Fatal(err)
		}
		exited := make(chan error, 1)

		m, err := serveWhenReady(watchReady(ln, exited, nil, nil), ln, exited, 50*time.Millisecond)
		if err != nil {
			t.Fatalf("serveWhenReady: %v, want the runtime to enter its loop anyway", err)
		}

		rt, captured := fakeRuntimeWithDeadline(t, []byte(getEvent), time.Now().Add(400*time.Millisecond))
		if err := handleInvocation(t.Context(), rt, m); err != nil {
			t.Fatalf("handleInvocation = %v, want the loop to carry on to the next invocation", err)
		}
		if got := captured.trailer.Get(headerErrorType); got != errTypeUpstream {
			t.Errorf("%s = %q, want %q", headerErrorType, got, errTypeUpstream)
		}
	})
}
