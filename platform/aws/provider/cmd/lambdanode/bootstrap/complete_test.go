package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func okNode(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	t.Cleanup(s.Close)
	return s
}

func TestHandleInvocationComplete(t *testing.T) {
	t.Run("holds until complete", func(t *testing.T) {
		node := okNode(t)
		rt, _ := fakeRuntime(t, []byte(getEvent))

		goSide, jsSide := net.Pipe()
		m := &nodeChild{
			nodePort:  portOf(t, node),
			client:    &http.Client{},
			control:   goSide,
			lifecycle: true,
			pending:   map[string]chan struct{}{},
		}
		go m.drainControl(bufio.NewReader(goSide))

		done := make(chan error, 1)
		go func() { done <- handleInvocation(context.Background(), rt, m) }()

		select {
		case err := <-done:
			t.Fatalf("handleInvocation returned before invocation-complete (err=%v)", err)
		case <-time.After(75 * time.Millisecond):
		}

		if _, err := jsSide.Write([]byte(`{"type":"invocation-complete","payload":{"requestId":"req-1"}}` + "\n")); err != nil {
			t.Fatalf("write control message: %v", err)
		}

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("handleInvocation: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("handleInvocation did not return after invocation-complete")
		}
	})

	t.Run("deadline forces progress", func(t *testing.T) {
		node := okNode(t)
		deadline := time.Now().Add(100 * time.Millisecond)
		rt := fakeRuntimeWithDeadline(t, []byte(getEvent), deadline)
		m := &nodeChild{
			nodePort:  portOf(t, node),
			client:    &http.Client{},
			lifecycle: true,
			pending:   map[string]chan struct{}{},
		}

		done := make(chan error, 1)
		go func() { done <- handleInvocation(context.Background(), rt, m) }()

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("handleInvocation: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("handleInvocation hung past the deadline with no completion signal")
		}

		m.mu.Lock()
		n := len(m.pending)
		m.mu.Unlock()
		if n != 0 {
			t.Errorf("pending waiters after timeout = %d, want 0", n)
		}
	})

	t.Run("an entrypoint that cannot signal completion is never waited on", func(t *testing.T) {
		node := okNode(t)
		deadline := time.Now().Add(30 * time.Second)
		rt := fakeRuntimeWithDeadline(t, []byte(getEvent), deadline)
		m := &nodeChild{
			nodePort: portOf(t, node),
			client:   &http.Client{},
			pending:  map[string]chan struct{}{},
		}

		done := make(chan error, 1)
		started := time.Now()
		go func() { done <- handleInvocation(context.Background(), rt, m) }()

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("handleInvocation: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("handleInvocation waited out the invocation budget for a node that never signals completion")
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Errorf("handleInvocation took %v, want it to return as soon as the response was written", elapsed)
		}
	})

	t.Run("unreachable node releases immediately", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		deadPort := l.Addr().(*net.TCPAddr).Port
		l.Close()

		rt := fakeRuntimeWithDeadline(t, []byte(getEvent), time.Now().Add(30*time.Second))
		m := &nodeChild{
			nodePort: deadPort,
			client:   &http.Client{},
			pending:  map[string]chan struct{}{},
		}

		done := make(chan error, 1)
		go func() { done <- handleInvocation(context.Background(), rt, m) }()

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("handleInvocation: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("handleInvocation stalled toward the deadline on an unreachable node")
		}

		m.mu.Lock()
		n := len(m.pending)
		m.mu.Unlock()
		if n != 0 {
			t.Errorf("pending waiters after release = %d, want 0", n)
		}
	})
}

func fakeRuntimeWithDeadline(t *testing.T, event []byte, deadline time.Time) *runtimeClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/"+runtimeAPIVersion+"/runtime/invocation/next", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Lambda-Runtime-Aws-Request-Id", "req-1")
		w.Header().Set("Lambda-Runtime-Invoked-Function-Arn", "arn:aws:lambda:us-east-1:123:function:fn")
		w.Header().Set("Lambda-Runtime-Deadline-Ms", strconv.FormatInt(deadline.UnixMilli(), 10))
		w.Write(event)
	})
	mux.HandleFunc("/"+runtimeAPIVersion+"/runtime/invocation/req-1/response", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusAccepted)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return newRuntimeClient(strings.TrimPrefix(srv.URL, "http://"))
}
