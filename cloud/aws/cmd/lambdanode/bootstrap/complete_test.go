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

// okNode is a loopback app that returns a small 200 body.
func okNode(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	t.Cleanup(s.Close)
	return s
}

// TestHandleInvocation_HoldsUntilComplete proves the runtime loop does not
// return (and therefore does not call the next /next) until the JS side sends
// invocation-complete over the control socket.
func TestHandleInvocation_HoldsUntilComplete(t *testing.T) {
	node := okNode(t)
	rt, _ := fakeRuntime(t, []byte(getEvent))

	// net.Pipe stands in for the JS↔Go control socket; jsSide writes the
	// control messages the entrypoint would emit.
	goSide, jsSide := net.Pipe()
	m := &Membrane{
		nodePort: portOf(t, node),
		client:   &http.Client{},
		control:  goSide,
		pending:  map[string]chan struct{}{},
	}
	go m.drainControl(bufio.NewReader(goSide))

	done := make(chan error, 1)
	go func() { done <- handleInvocation(context.Background(), rt, m) }()

	// Must still be blocked: the body is delivered but completion hasn't fired.
	select {
	case err := <-done:
		t.Fatalf("handleInvocation returned before invocation-complete (err=%v)", err)
	case <-time.After(75 * time.Millisecond):
	}

	// The JS entrypoint reports completion for the request forward() tagged.
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
}

// TestHandleInvocation_DeadlineForcesProgress proves that when no completion
// signal ever arrives, the deadline (minus the margin) still releases the loop
// instead of hanging forever.
func TestHandleInvocation_DeadlineForcesProgress(t *testing.T) {
	node := okNode(t)
	// A deadline in the near past-relative-to-margin: time.Until(deadline) is
	// under completionMargin, so the wait clamps to zero and returns promptly.
	deadline := time.Now().Add(100 * time.Millisecond)
	rt := fakeRuntimeWithDeadline(t, []byte(getEvent), deadline)
	m := &Membrane{
		nodePort: portOf(t, node),
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
		t.Fatal("handleInvocation hung past the deadline with no completion signal")
	}

	// The waiter registration must be cleaned up on the timeout path.
	m.mu.Lock()
	n := len(m.pending)
	m.mu.Unlock()
	if n != 0 {
		t.Errorf("pending waiters after timeout = %d, want 0", n)
	}
}

// TestHandleInvocation_UnreachableNodeReleasesImmediately proves that when the
// request never reaches Node (so no invocation-complete can ever arrive), the
// loop releases at once instead of stalling toward the deadline.
func TestHandleInvocation_UnreachableNodeReleasesImmediately(t *testing.T) {
	// Reserve then free a port so nothing is listening → connection refused.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadPort := l.Addr().(*net.TCPAddr).Port
	l.Close()

	// A generous deadline: if the wait were bounded only by the deadline, this
	// test would take ~30s. Releasing promptly proves the unreachable path fires.
	rt := fakeRuntimeWithDeadline(t, []byte(getEvent), time.Now().Add(30*time.Second))
	m := &Membrane{
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
}

// fakeRuntimeWithDeadline stands up a Runtime API that serves one invocation
// carrying a Lambda-Runtime-Deadline-Ms header, and swallows the streamed
// response.
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
