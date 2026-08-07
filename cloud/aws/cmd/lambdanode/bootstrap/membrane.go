package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// startupBudget bounds init as a whole: the local decrypts and then the wait for
// node to announce itself, which gets whatever they left. A crashed child is
// caught by the reaper the moment it exits, so the second half only has to cover
// the child that is alive but wedged. The total sits under Lambda's ~10s init
// ceiling, leaving room to report a real init error before the platform kills
// the sandbox and says nothing useful in its place.
const startupBudget = 8 * time.Second

const nodeBinaryPath = "/var/lang/bin/node"

type Membrane struct {
	nodePort int
	control  net.Conn
	client   *http.Client

	// live is this execution environment's live-class values, shared by every
	// invocation it serves. Nil for a function that declares none.
	live *liveValues

	// bytecode is this instance's compile-cache upload, nil for a deployment
	// that is not configured for one. It is installed before the runtime loop
	// starts and never reassigned.
	bytecode *bytecodeUpload

	// pending maps an in-flight request id to the channel closed when the JS
	// side reports the invocation complete (response finished and every
	// waitUntil promise settled). Nil in tests that don't exercise completion.
	mu           sync.Mutex
	pending      map[string]chan struct{}
	flushWaiter  chan compileCacheFlushedPayload
	bytecodeDone bool
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

// flushCompileCacheLine is the request node answers with a
// compile-cache-flushed message. It is a constant rather than a marshalled
// struct because it has no fields and never will.
const flushCompileCacheLine = `{"type":"flush-compile-cache"}` + "\n"

// flushCompileCache asks node to write its compile cache to disk and waits for
// the ack. A child that does not answer within the cap gets no upload rather
// than a runtime loop parked on it.
//
// The request goes out as a single Write: live values are pushed down this same
// connection from another goroutine, and Go serializes a net.Conn's writes per
// call, so one call per line is what stops the two interleaving mid-message.
func (m *Membrane) flushCompileCache(ctx context.Context) (compileCacheFlushedPayload, bool) {
	if m.control == nil {
		return compileCacheFlushedPayload{}, false
	}
	ack := make(chan compileCacheFlushedPayload, 1)
	m.mu.Lock()
	m.flushWaiter = ack
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.flushWaiter = nil
		m.mu.Unlock()
	}()

	if err := m.writeFlushRequest(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "ocel: could not ask node to flush its compile cache: %v\n", err)
		return compileCacheFlushedPayload{}, false
	}

	timer := time.NewTimer(compileCacheFlushTimeout)
	defer timer.Stop()
	select {
	case p := <-ack:
		return p, true
	case <-timer.C:
	case <-ctx.Done():
	}
	fmt.Fprintln(os.Stderr, "ocel: node did not acknowledge the compile-cache flush; skipping upload")
	return compileCacheFlushedPayload{}, false
}

// writeFlushRequest sends the request under a deadline. A child wedged with a
// full receive buffer would otherwise block this write indefinitely — past the
// budget and past the invocation deadline — which is the one way this path
// could still cost an already-answered invocation a recorded timeout.
//
// The deadline is cleared immediately after, because live values are pushed
// down this same connection and must not inherit it. The window where one could
// is a single syscall wide, and a live push caught in it fails, logs and is
// retried by the next refresh.
func (m *Membrane) writeFlushRequest(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(compileCacheFlushTimeout)
	}
	if err := m.control.SetWriteDeadline(deadline); err != nil {
		return err
	}
	_, err := m.control.Write([]byte(flushCompileCacheLine))
	m.control.SetWriteDeadline(time.Time{})
	return err
}

// deliverCompileCacheFlush hands the ack to whoever asked for it, and drops it
// when nobody did — an ack that arrives after its waiter gave up has no reader,
// and the buffered channel keeps this from ever blocking the drain loop.
func (m *Membrane) deliverCompileCacheFlush(p compileCacheFlushedPayload) {
	m.mu.Lock()
	ack := m.flushWaiter
	m.flushWaiter = nil
	m.mu.Unlock()
	if ack != nil {
		ack <- p
	}
}

// uploadBytecodeCacheOnce publishes the compile cache the first time it is
// called and never again on this instance, whatever the outcome was. A cache is
// only worth uploading once — every later invocation would rebuild and re-HEAD
// the same object — and an attempt that failed is evidence this instance cannot
// do it, not a reason to retry on the next request's billed time.
func (m *Membrane) uploadBytecodeCacheOnce(ctx context.Context) {
	if m.bytecode == nil {
		return
	}
	m.mu.Lock()
	done := m.bytecodeDone
	m.bytecodeDone = true
	m.mu.Unlock()
	if done {
		return
	}
	m.bytecode.run(ctx)
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

// compileCacheFlushedPayload is node's answer to a flush request. Dir is the
// directory it actually wrote to, and ok is false whenever there is nothing
// worth uploading — including on a node too old to know the API at all, which
// is what keeps a version check out of this package.
type compileCacheFlushedPayload struct {
	Dir string `json:"dir"`
	OK  bool   `json:"ok"`
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
//
// abandon is the fourth outcome: init work node itself is waiting on has
// failed, so node is never going to announce anything and the rest of the
// budget is dead time the caller needs to report a diagnosis in. Nil means
// there is no such work, and the wait ends only on the other three.
func awaitReady(ln net.Listener, exited <-chan error, budget time.Duration, onControl func(io.Writer), abandon <-chan struct{}) (*nodeReady, error) {
	type result struct {
		ready *nodeReady
		err   error
	}
	var log lastLog
	done := make(chan result, 1)
	go func() {
		ready, err := handshake(ln, &log, onControl)
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
	case <-abandon:
		ln.Close()
		return nil, fmt.Errorf("node was left waiting on init work that failed%s", log.suffix())
	case <-time.After(budget):
		ln.Close()
		return nil, fmt.Errorf("node did not signal ready within %s%s", budget, log.suffix())
	}
}

// handshake accepts node's control connection and reads until it announces its
// HTTP port, recording any log it reports on the way.
//
// onControl runs the moment the connection exists, not when node reports
// ready: the socket is the only channel the membrane can push a value down, and
// node connects it before importing the application, so anything the membrane
// has resolved by then can reach module-scope code that asks for it.
func handshake(ln net.Listener, log *lastLog, onControl func(io.Writer)) (*nodeReady, error) {
	control, err := ln.Accept()
	if err != nil {
		return nil, err
	}
	if onControl != nil {
		onControl(control)
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

// nodeChildEnv is the environment the child is exec'd with, split out from the
// spawn so what node is told can be asserted without a node binary.
func nodeChildEnv(sockPath string, extraEnv []string) []string {
	env := append(os.Environ(),
		"OCEL_CONTROL_SOCKET="+sockPath,
		"OCEL_HANDLER="+os.Getenv("OCEL_HANDLER"), // user's compiled entry
	)
	env = append(env, compileCacheEnv()...)
	return append(env, extraEnv...)
}

func entrypointPath() string {
	const nodeEntry = "/opt/ocel/node/entrypoint.mjs"
	data, err := os.ReadFile(filepath.Join(taskRoot(), "config.json"))
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
//
// extraEnv is what can only reach node through the environment the child is
// exec'd with — a one-shot channel that closes at exec, which is why the config
// and baked-bundle work it carries is not deferred past here.
//
// It is no longer the only channel. onControl hands out node's control
// connection as soon as it exists, which is what a live-class value is pushed
// down: a value fetched while this function was still exec'ing and waiting has
// somewhere to go, so its fetch overlaps the spawn instead of preceding it.
//
// abandon ends that wait early when the fetch fails, because node holds its
// import until the push arrives and would otherwise sit here until the budget
// ran out.
func startNode(extraEnv []string, budget time.Duration, onControl func(io.Writer), abandon <-chan struct{}) (*Membrane, error) {
	// TODO: randomize
	sockPath := "/tmp/ocel-control.sock"
	_ = os.Remove(sockPath) // stale socket from a reused sandbox

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(nodeBinaryPath, entrypointPath())
	cmd.Env = nodeChildEnv(sockPath, extraEnv)
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
	ready, err := awaitReady(ln, exited, budget, onControl, abandon)
	ln.Close()
	if err != nil {
		return nil, err
	}

	m := &Membrane{
		control:  ready.control,
		nodePort: ready.httpPort,
		pending:  map[string]chan struct{}{},
		client:   newLoopbackClient(),
	}

	// Keep draining control messages (logs, metrics, completion) in the background.
	go m.drainControl(ready.reader)
	go superviseNode(exited)
	return m, nil
}

// newLoopbackClient builds the data-plane client every forward runs through:
// plain loopback TCP, with the transport tuned for the single-client,
// keep-alive-to-one-host case.
func newLoopbackClient() *http.Client {
	return &http.Client{
		// A 3xx from the app is the response, not an instruction to this proxy.
		// Following it would swallow the redirect and answer with the target's
		// body under a 200 — and a relative Location would re-enter the app at
		// another path, serving that route's content under the requested URL.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			MaxIdleConns:        16,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     90 * time.Second,
		},
	}
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
		case "compile-cache-flushed":
			var p compileCacheFlushedPayload
			if json.Unmarshal(msg.Payload, &p) == nil {
				m.deliverCompileCacheFlush(p)
			}
		}
	}
}
