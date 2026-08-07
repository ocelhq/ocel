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

// minSpawnBudget is the least bringUp will ever hand the spawn, even when
// pre-spawn work (the resolve join, rehydration) already ate past
// startupBudget by the time it's subtracted. Without a floor, a non-positive
// budget turns into awaitReady's time.After(negative) firing immediately — a
// spurious "node did not signal ready" when node was never given a chance to
// start. The floor deliberately lets that worst case spill init past
// startupBudget into the ~2s of headroom below Lambda's ~10s init ceiling: a
// boot that would otherwise fail outright is worth more than holding every
// run under 8s, and the normal carve-out (rehydration's cost subtracted from
// startupBudget) is unaffected outside this pathological, slow-miss case.
const minSpawnBudget = 4 * time.Second

const nodeBinaryPath = "/var/lang/bin/node"

type Membrane struct {
	nodePort int
	control  net.Conn
	client   *http.Client

	// live is this execution environment's live-class values, shared by every
	// invocation it serves. Nil for a function that declares none.
	live *liveValues

	// bytecode is this instance's compile-cache upload, nil for a deployment
	// that is not configured for one and also nil for one that rehydrated the
	// object at init — a hit already proves it exists, so there is nothing
	// left for this instance's upload to do. It is installed before the
	// runtime loop starts and never reassigned.
	bytecode *bytecodeUpload

	// bytecodeSource records that the nil above is a hit rather than a
	// deployment with no compile cache at all, and which leg produced it. The
	// two are the same absence to every other caller, and only a warm
	// invocation has to tell them apart — which it cannot do by re-resolving
	// without paying for the resolution a second time. It is the source rather
	// than a flag beside one so the two can never disagree; empty means this
	// instance never ran the legs at all.
	bytecodeSource bytecodeSource

	// bytecodeKey is the key the resolution composed, kept here because a hit
	// leaves bytecode nil and a warm invocation still has to report the key: a
	// deploy cannot compose it, having never learned node's version. Copied
	// from the resolution, never recomposed.
	bytecodeKey string

	// pending maps an in-flight request id to the channel closed when the JS
	// side reports the invocation complete (response finished and every
	// waitUntil promise settled). Nil in tests that don't exercise completion.
	mu           sync.Mutex
	pending      map[string]chan struct{}
	flushWaiter  chan compileCacheFlushedPayload
	warmWaiter   chan compileCacheWarmedPayload
	bytecodeDone bool
}

// bytecodeCacheSource is bytecodeSource as anything outside init should read
// it: a Membrane that never ran the legs — every unit test of the data plane
// builds one — reports none rather than an empty string a deploy would have to
// interpret.
func (m *Membrane) bytecodeCacheSource() bytecodeSource {
	if m.bytecodeSource == "" {
		return bytecodeSourceNone
	}
	return m.bytecodeSource
}

// bytecodeCached reports whether init found this deployment's cache already
// published. It is derived from the source rather than tracked beside it,
// which is what keeps "there was a hit" and "here is where it came from" from
// ever answering differently.
func (m *Membrane) bytecodeCached() bool {
	return m.bytecodeCacheSource() != bytecodeSourceNone
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

func (m *Membrane) writeFlushRequest(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(compileCacheFlushTimeout)
	}
	return m.writeControlRequest([]byte(flushCompileCacheLine), deadline)
}

// writeControlRequest sends one request line under a deadline. A child wedged
// with a full receive buffer would otherwise block this write indefinitely —
// past the budget and past the invocation deadline — which is the one way this
// path could still cost an already-answered invocation a recorded timeout.
//
// The deadline is cleared immediately after, because live values are pushed
// down this same connection and must not inherit it. The window where one could
// is a single syscall wide, and a live push caught in it fails, logs and is
// retried by the next refresh.
func (m *Membrane) writeControlRequest(line []byte, deadline time.Time) error {
	if err := m.control.SetWriteDeadline(deadline); err != nil {
		return err
	}
	_, err := m.control.Write(line)
	m.control.SetWriteDeadline(time.Time{})
	return err
}

// warmCompileCache asks node to load every entry in the bundle and waits for
// what that produced. The deadline travels in the request rather than being
// derived on the node side, because only the membrane knows what the publish
// leg after it still needs; the ceiling travels with it for the same reason,
// so the number both legs are judged against has one home.
//
// The wait ends at the load deadline — the request's own is the reply margin
// earlier, so a node that stops exactly when it was told still has that long
// for its answer to arrive.
//
// The waiter comes back alongside the report, still registered, because node
// checks the deadline only between entries: one overrunning require answers
// late, and the caller's publish leg is a second window for that answer to
// arrive in. Whoever asks owns endWarmExchange.
func (m *Membrane) warmCompileCache(ctx context.Context, deadline time.Time) (compileCacheWarmedPayload, <-chan compileCacheWarmedPayload, bool) {
	if m.control == nil {
		return compileCacheWarmedPayload{}, nil, false
	}
	reply := make(chan compileCacheWarmedPayload, 1)
	m.mu.Lock()
	m.warmWaiter = reply
	m.mu.Unlock()

	// The write's own bound is about the child draining its socket now, not
	// about how long the work it is being handed may take.
	if err := m.writeControlRequest(warmCompileCacheLine(deadline), time.Now().Add(compileCacheFlushTimeout)); err != nil {
		fmt.Fprintf(os.Stderr, "ocel: could not ask node to warm its compile cache: %v\n", err)
		return compileCacheWarmedPayload{}, nil, false
	}

	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	select {
	case p := <-reply:
		return p, reply, true
	case <-ctx.Done():
	}
	fmt.Fprintln(os.Stderr, "ocel: node did not report back on the compile-cache warm")
	return compileCacheWarmedPayload{}, reply, false
}

// collectWarmReport takes a report that landed after the wait gave up, without
// blocking for one that still has not.
func collectWarmReport(waiter <-chan compileCacheWarmedPayload) (compileCacheWarmedPayload, bool) {
	select {
	case p := <-waiter:
		return p, true
	default:
		return compileCacheWarmedPayload{}, false
	}
}

// endWarmExchange stops the drain loop delivering to a waiter nobody is left to
// read, which would park it and with it invocation-complete.
func (m *Membrane) endWarmExchange() {
	m.mu.Lock()
	m.warmWaiter = nil
	m.mu.Unlock()
}

// warmReplyMargin is held back from the deadline node is told, so a child that
// loads right up to its instruction still has time to answer before the
// membrane stops listening. Without it the two ends race on the same instant,
// and a pass that loaded the whole bundle could be reported as one node never
// answered.
const warmReplyMargin = 250 * time.Millisecond

// warmCompileCacheLine is the request, marshalled rather than written out as a
// constant the way the flush is: it carries fields, and a hand-composed line
// would be one more place the ceiling could drift from bytecodeCacheCeiling.
func warmCompileCacheLine(deadline time.Time) []byte {
	line, _ := json.Marshal(warmCompileCacheRequest{
		Type: "warm-compile-cache",
		Payload: warmCompileCacheParams{
			DeadlineMs:   deadline.Add(-warmReplyMargin).UnixMilli(),
			CeilingBytes: bytecodeCacheCeiling,
		},
	})
	return append(line, '\n')
}

type warmCompileCacheRequest struct {
	Type    string                 `json:"type"`
	Payload warmCompileCacheParams `json:"payload"`
}
type warmCompileCacheParams struct {
	DeadlineMs   int64 `json:"deadlineMs"`
	CeilingBytes int64 `json:"ceilingBytes"`
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

// deliverCompileCacheWarm is deliverCompileCacheFlush's twin, and drops an
// unawaited reply for the same reason: a report that arrives after its waiter
// gave up has no reader, and the drain loop also carries invocation-complete.
func (m *Membrane) deliverCompileCacheWarm(p compileCacheWarmedPayload) {
	m.mu.Lock()
	reply := m.warmWaiter
	m.warmWaiter = nil
	m.mu.Unlock()
	if reply != nil {
		reply <- p
	}
}

// claimBytecodeUpload reports whether the caller owns the one upload attempt
// this instance gets. A cache is only worth uploading once — every later
// invocation would rebuild and re-HEAD the same object — and an attempt that
// failed is evidence this instance cannot do it, not a reason to retry on the
// next request's billed time. A warm invocation claims it too, so the pass it
// already spent inline is not spent again after the next request.
func (m *Membrane) claimBytecodeUpload() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	claimed := !m.bytecodeDone
	m.bytecodeDone = true
	return claimed
}

// uploadBytecodeCacheOnce publishes the compile cache the first time it is
// called and never again on this instance, whatever the outcome was.
func (m *Membrane) uploadBytecodeCacheOnce(ctx context.Context) {
	if m.bytecode == nil || !m.claimBytecodeUpload() {
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

// compileCacheWarmedPayload is node's report on a warm pass. ok is false —
// with state "unsupported" — on an artifact that has no warm capability at
// all, which is a bundle whose walk went unaccounted for rather than one with
// nothing on disk to publish.
//
// Skipped names what the walk never reached and SkippedCount says how many
// there were, because the list node sends is bounded: an operator reading
// "38/51" has no way to tell which routes stay cold, which is the one thing
// this report exists to tell them. node's own "dir" is read by nobody here —
// the flush ack the upload leg waits on is what names the directory it
// archives — so it is not a field.
type compileCacheWarmedPayload struct {
	OK           bool          `json:"ok"`
	State        string        `json:"state"`
	Entries      int           `json:"entries"`
	Loaded       int           `json:"loaded"`
	Failures     []warmFailure `json:"failures"`
	StoppedBy    string        `json:"stoppedBy"`
	Skipped      []string      `json:"skipped"`
	SkippedCount int           `json:"skippedCount"`
	Bytes        int64         `json:"bytes"`
}

// warmFailure is one bundle entry that would not load. They travel back rather
// than being counted, because a route that throws on import is a bug the
// deploy's operator wants named, not a number.
type warmFailure struct {
	Entry   string `json:"entry"`
	Message string `json:"message"`
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
		case "compile-cache-warmed":
			var p compileCacheWarmedPayload
			if json.Unmarshal(msg.Payload, &p) == nil {
				m.deliverCompileCacheWarm(p)
			}
		}
	}
}
