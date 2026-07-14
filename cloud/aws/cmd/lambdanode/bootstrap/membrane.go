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

func startNode() (*Membrane, error) {
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
	cmd.Stdout = os.Stdout // Node stdout → CloudWatch
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Node connects back to our control socket.
	control, err := ln.Accept()
	if err != nil {
		return nil, err
	}

	m := &Membrane{control: control, pending: map[string]chan struct{}{}}

	// Read control messages until "server-ready" gives us the port.
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
		if msg.Type == "server-ready" {
			var p serverReadyPayload
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				return nil, err
			}
			m.nodePort = p.HTTPPort
			break
		}
	}

	// Data-plane client: plain loopback TCP. Tune the transport for the
	// single-client, keep-alive-to-one-host case.
	m.client = &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        16,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	// Keep draining control messages (logs, metrics, completion) in the background.
	go m.drainControl(reader)
	return m, nil
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
