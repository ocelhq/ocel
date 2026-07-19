package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// completionMargin is how far before the invocation deadline we stop waiting
// for background (waitUntil) tasks, so the runtime can cleanly call /next
// before Lambda hard-kills the sandbox.
const completionMargin = 500 * time.Millisecond

// startupBudget bounds init as a whole: the globally bootstrapped config fetch
// (configBudget) and then the wait for node to announce itself, which gets
// whatever the fetch left. A crashed child is caught by the reaper the moment it
// exits, so the second half only has to cover the child that is alive but
// wedged. The total sits under Lambda's ~10s init ceiling, leaving room to
// report a real init error before the platform kills the sandbox and says
// nothing useful in its place.
const startupBudget = 8 * time.Second

type Membrane struct {
	nodePort int
	control  net.Conn
	client   *http.Client

	// pending maps an in-flight request id to the channel closed when the JS
	// side reports the invocation complete (response finished and every
	// waitUntil promise settled). Nil in tests that don't exercise completion.
	mu      sync.Mutex
	pending map[string]chan struct{}
}

// registerWaiter records interest in an invocation's completion signal and
// returns a channel closed when it arrives. It must be called before the
// request is forwarded so a fast completion can't be missed. A Membrane without
// a completion map (unit tests of the data plane) returns nil, meaning "don't
// wait".
func (m *Membrane) registerWaiter(requestID string) <-chan struct{} {
	if m.pending == nil {
		return nil
	}
	ch := make(chan struct{})
	m.mu.Lock()
	m.pending[requestID] = ch
	m.mu.Unlock()
	return ch
}

// dropWaiter removes and returns the waiter for requestID, if one is
// registered. It is the single owner of deletes from m.pending.
func (m *Membrane) dropWaiter(requestID string) (chan struct{}, bool) {
	m.mu.Lock()
	ch, ok := m.pending[requestID]
	if ok {
		delete(m.pending, requestID)
	}
	m.mu.Unlock()
	return ch, ok
}

// signalComplete unblocks awaitCompletion for requestID (no-op if already
// completed or timed out).
func (m *Membrane) signalComplete(requestID string) {
	if ch, ok := m.dropWaiter(requestID); ok {
		close(ch)
	}
}

// awaitCompletion blocks until the invocation is reported complete or the
// deadline (minus completionMargin) elapses, holding off the next /next so the
// sandbox stays warm for background tasks. A nil waiter returns immediately.
// With no deadline (only reachable off-Lambda; the Runtime API always sets one)
// it waits on the completion signal alone.
func (m *Membrane) awaitCompletion(ctx context.Context, requestID string, waiter <-chan struct{}) {
	if waiter == nil {
		return
	}
	var timeout <-chan time.Time
	if deadline, ok := ctx.Deadline(); ok {
		d := time.Until(deadline) - completionMargin
		if d < 0 {
			d = 0
		}
		t := time.NewTimer(d)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-waiter:
	case <-timeout:
		m.dropWaiter(requestID)
		fmt.Fprintf(os.Stderr, "ocel: background tasks abandoned for %s: deadline reached\n", requestID)
	}
}

type controlMsg struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}
type invocationCompletePayload struct {
	RequestID string `json:"requestId"`
}
type serverReadyPayload struct {
	HTTPPort int `json:"httpPort"`
}
type logPayload struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

// lastLog holds the most recent log line node reported during startup. The
// handshake writes it while awaitReady may be reading it to explain a failure,
// so access is guarded.
type lastLog struct {
	mu  sync.Mutex
	msg string
}

func (l *lastLog) set(msg string) {
	l.mu.Lock()
	l.msg = msg
	l.mu.Unlock()
}

// suffix renders what node last said as an error tail, empty if it said
// nothing. Whatever node reported before failing is usually the diagnosis, so
// it travels with the error rather than being dropped for a bare exit status.
func (l *lastLog) suffix() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.msg == "" {
		return ""
	}
	return "; last log from node: " + l.msg
}

type nodeReady struct {
	control  net.Conn
	reader   *bufio.Reader
	httpPort int
}

// awaitReady waits for node to connect to the control socket and announce its
// HTTP port. The handshake blocks on a socket node may never touch, so it runs
// on its own goroutine and the wait ends on whichever comes first: ready, the
// child exiting (the usual failure — a throw on import), or the budget expiring
// (alive but wedged). Any outcome but ready is returned as an error, which the
// caller reports as an init failure. Without this the wait is unbounded and a
// dead child hangs the sandbox until Lambda kills it, logging nothing.
func awaitReady(ln net.Listener, exited <-chan error, budget time.Duration) (*nodeReady, error) {
	type result struct {
		ready *nodeReady
		err   error
	}
	var log lastLog
	done := make(chan result, 1)
	go func() {
		ready, err := handshake(ln, &log)
		done <- result{ready: ready, err: err}
	}()

	// Closing the listener unblocks a handshake still parked in Accept.
	select {
	case r := <-done:
		if r.err != nil {
			return nil, fmt.Errorf("node control handshake failed: %w%s", r.err, log.suffix())
		}
		return r.ready, nil
	case err := <-exited:
		ln.Close()
		return nil, fmt.Errorf("node exited before signalling ready: %w%s", err, log.suffix())
	case <-time.After(budget):
		ln.Close()
		return nil, fmt.Errorf("node did not signal ready within %s%s", budget, log.suffix())
	}
}

// handshake accepts node's control connection and reads until it announces its
// HTTP port, recording any log it reports on the way.
func handshake(ln net.Listener, log *lastLog) (*nodeReady, error) {
	control, err := ln.Accept()
	if err != nil {
		return nil, err
	}
	reader := bufio.NewReader(control)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		var msg controlMsg
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "log":
			var p logPayload
			if json.Unmarshal(msg.Payload, &p) == nil && p.Message != "" {
				log.set(p.Message)
			}
		case "server-ready":
			var p serverReadyPayload
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				return nil, err
			}
			return &nodeReady{control: control, reader: reader, httpPort: p.HTTPPort}, nil
		}
	}
}

func entrypointPath() string {
	const nodeEntry = "/opt/ocel/node/entrypoint.mjs"
	root := os.Getenv("LAMBDA_TASK_ROOT")
	if root == "" {
		root = "/var/task"
	}
	data, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		return nodeEntry
	}
	var cfg struct {
		Framework string `json:"framework"`
	}
	if json.Unmarshal(data, &cfg) == nil && cfg.Framework == "next" {
		return "/opt/ocel/next/entrypoint.mjs"
	}
	return nodeEntry
}

// startNode execs the node child and waits budget for it to announce itself.
// extraEnv is the globally bootstrapped config resolved before this point; it
// can only reach node through the environment the child is exec'd with, which is
// why the fetch is not deferred past here.
func startNode(extraEnv []string, budget time.Duration) (*Membrane, error) {
	// TODO: randomize
	sockPath := "/tmp/ocel-control.sock"
	_ = os.Remove(sockPath) // stale socket from a reused sandbox

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("/var/lang/bin/node", entrypointPath())
	cmd.Env = append(os.Environ(),
		"OCEL_CONTROL_SOCKET="+sockPath,
		"OCEL_HANDLER="+os.Getenv("OCEL_HANDLER"), // user's compiled entry
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	cmd.Stdout = os.Stdout // Node stdout → CloudWatch
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Reap the child so its death is a signal rather than a silent stall: this
	// is what turns "the app threw on import" from an unbounded wait into an
	// immediate, attributable init error.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	// Node connects back to our control socket and announces its port. Only one
	// connection is ever accepted, so the listener is spent either way.
	ready, err := awaitReady(ln, exited, budget)
	ln.Close()
	if err != nil {
		return nil, err
	}

	m := &Membrane{
		control:  ready.control,
		nodePort: ready.httpPort,
		pending:  map[string]chan struct{}{},

		// Data-plane client: plain loopback TCP. Tune the transport for the
		// single-client, keep-alive-to-one-host case.
		client: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        16,
				MaxIdleConnsPerHost: 16,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}

	// Keep draining control messages (logs, metrics, completion) in the background.
	go m.drainControl(ready.reader)
	go superviseNode(exited)
	return m, nil
}

// superviseNode ends the process once node dies after a successful start. A
// sandbox outlives the invocation that warmed it, so a runtime left holding a
// dead node would take every request routed to it and fail each one; exiting
// lets Lambda replace the sandbox instead.
func superviseNode(exited <-chan error) {
	err := <-exited
	fmt.Fprintf(os.Stderr, "ocel: node exited after startup: %v\n", err)
	os.Exit(1)
}

func (m *Membrane) drainControl(reader *bufio.Reader) {
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return // Node died; sandbox will be recycled by Lambda
		}
		var msg controlMsg
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "log":
			// forward to Ocel's log pipeline
		case "metric":
			// forward to Ocel telemetry
		case "request-end":
			// per-request telemetry (status/duration) from the JS wrapper
		case "invocation-complete":
			// response finished and every waitUntil promise settled; release
			// the runtime loop to call /next and let the sandbox freeze.
			var p invocationCompletePayload
			if json.Unmarshal(msg.Payload, &p) == nil {
				m.signalComplete(p.RequestID)
			}
		}
	}
}
