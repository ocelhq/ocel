package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/runtime/bytecode"
)

const completionMargin = 500 * time.Millisecond

const startupBudget = 8 * time.Second

const minSpawnBudget = 4 * time.Second

const nodeBinaryPath = "/var/lang/bin/node"

type nodeChild struct {
	upstream

	ready chan struct{}

	control net.Conn

	live liveValues

	cache compileCache

	lifecycle bool

	mu           sync.Mutex
	pending      map[string]chan struct{}
	flushWaiter  chan compileCacheFlushedPayload
	warmWaiter   chan compileCacheWarmedPayload
	bytecodeDone bool
}

func (m *nodeChild) cacheSource() bytecode.Source {
	if m.cache == nil {
		return bytecode.SourceNone
	}
	return m.cache.Source()
}

func (m *nodeChild) cached() bool {
	return m.cacheSource() != bytecode.SourceNone
}

func (m *nodeChild) refreshLiveValues(ctx context.Context) {
	if m.live == nil {
		return
	}
	m.live.Refresh(ctx)
}

func (m *nodeChild) endInvocation(ctx context.Context, requestID string, waiter <-chan struct{}, reached bool) {
	if !reached {
		m.signalComplete(requestID)
	}
	m.awaitCompletion(ctx, requestID, waiter)
	m.uploadBytecodeCacheOnce(ctx)
}

func (m *nodeChild) beginInvocation(requestID string) <-chan struct{} {
	if m.pending == nil || !m.lifecycle {
		return nil
	}
	ch := make(chan struct{})
	m.mu.Lock()
	m.pending[requestID] = ch
	m.mu.Unlock()
	return ch
}

func (m *nodeChild) dropWaiter(requestID string) (chan struct{}, bool) {
	m.mu.Lock()
	ch, ok := m.pending[requestID]
	if ok {
		delete(m.pending, requestID)
	}
	m.mu.Unlock()
	return ch, ok
}

func (m *nodeChild) signalComplete(requestID string) {
	if ch, ok := m.dropWaiter(requestID); ok {
		close(ch)
	}
}

func (m *nodeChild) awaitCompletion(ctx context.Context, requestID string, waiter <-chan struct{}) {
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

const compileCacheFlushTimeout = time.Second

const flushCompileCacheLine = `{"type":"flush-compile-cache"}` + "\n"

func (m *nodeChild) flushCompileCache(ctx context.Context) (compileCacheFlushedPayload, bool) {
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

func (m *nodeChild) writeFlushRequest(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(compileCacheFlushTimeout)
	}
	return m.writeControlRequest([]byte(flushCompileCacheLine), deadline)
}

func (m *nodeChild) writeControlRequest(line []byte, deadline time.Time) error {
	if err := m.control.SetWriteDeadline(deadline); err != nil {
		return err
	}
	_, err := m.control.Write(line)
	_ = m.control.SetWriteDeadline(time.Time{})
	return err
}

func (m *nodeChild) warmCompileCache(ctx context.Context, deadline time.Time) (compileCacheWarmedPayload, <-chan compileCacheWarmedPayload, bool) {
	if m.control == nil {
		return compileCacheWarmedPayload{}, nil, false
	}
	reply := make(chan compileCacheWarmedPayload, 1)
	m.mu.Lock()
	m.warmWaiter = reply
	m.mu.Unlock()

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

func collectWarmReport(waiter <-chan compileCacheWarmedPayload) (compileCacheWarmedPayload, bool) {
	select {
	case p := <-waiter:
		return p, true
	default:
		return compileCacheWarmedPayload{}, false
	}
}

func (m *nodeChild) endWarmExchange() {
	m.mu.Lock()
	m.warmWaiter = nil
	m.mu.Unlock()
}

const warmReplyMargin = 250 * time.Millisecond

func warmCompileCacheLine(deadline time.Time) []byte {
	line, _ := json.Marshal(warmCompileCacheRequest{
		Type: "warm-compile-cache",
		Payload: warmCompileCacheParams{
			DeadlineMs:   deadline.Add(-warmReplyMargin).UnixMilli(),
			CeilingBytes: bytecode.CacheCeiling,
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

func (m *nodeChild) deliverCompileCacheFlush(p compileCacheFlushedPayload) {
	m.mu.Lock()
	ack := m.flushWaiter
	m.flushWaiter = nil
	m.mu.Unlock()
	if ack != nil {
		ack <- p
	}
}

func (m *nodeChild) deliverCompileCacheWarm(p compileCacheWarmedPayload) {
	m.mu.Lock()
	reply := m.warmWaiter
	m.warmWaiter = nil
	m.mu.Unlock()
	if reply != nil {
		reply <- p
	}
}

func (m *nodeChild) claimBytecodeUpload() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	claimed := !m.bytecodeDone
	m.bytecodeDone = true
	return claimed
}

func (m *nodeChild) uploadBytecodeCacheOnce(ctx context.Context) {
	if m.cache == nil || m.cache.Cached() || !m.claimBytecodeUpload() {
		return
	}
	m.cache.Upload(ctx, uploadBy(ctx), m.flushed)
}

func uploadBy(ctx context.Context) time.Time {
	deadline, ok := ctx.Deadline()
	if !ok {
		return time.Time{}
	}
	return deadline.Add(-completionMargin)
}

func (m *nodeChild) flushed(ctx context.Context) (bytecode.Flushed, bool) {
	p, ok := m.flushCompileCache(ctx)
	return bytecode.Flushed{Dir: p.Dir, OK: p.OK}, ok
}

type controlMsg struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}
type invocationCompletePayload struct {
	RequestID string `json:"requestId"`
}
type serverReadyPayload struct {
	HTTPPort  int  `json:"httpPort"`
	Lifecycle bool `json:"lifecycle"`
}

type compileCacheFlushedPayload struct {
	Dir string `json:"dir"`
	OK  bool   `json:"ok"`
}

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

type warmFailure struct {
	Entry   string `json:"entry"`
	Message string `json:"message"`
}

type logPayload struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

type lastLog struct {
	mu  sync.Mutex
	msg string
}

func (l *lastLog) set(msg string) {
	l.mu.Lock()
	l.msg = msg
	l.mu.Unlock()
}

func (l *lastLog) suffix() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.msg == "" {
		return ""
	}
	return "; last log from node: " + l.msg
}

type nodeReady struct {
	control   net.Conn
	reader    *bufio.Reader
	httpPort  int
	lifecycle bool
}

type readyResult struct {
	ready *nodeReady
	err   error
}

func watchReady(ln net.Listener, exited <-chan error, onControl func(io.Writer), abandon <-chan struct{}) <-chan readyResult {
	var log lastLog
	shook := make(chan readyResult, 1)
	go func() {
		ready, err := handshake(ln, &log, onControl)
		shook <- readyResult{ready: ready, err: err}
	}()

	out := make(chan readyResult, 1)
	go func() {
		select {
		case r := <-shook:
			if r.err != nil {
				out <- readyResult{err: fmt.Errorf("node control handshake failed: %w%s", r.err, log.suffix())}
				return
			}
			out <- r
		case err := <-exited:
			ln.Close()
			out <- readyResult{err: fmt.Errorf("node exited before signalling ready: %w%s", err, log.suffix())}
		case <-abandon:
			ln.Close()
			out <- readyResult{err: fmt.Errorf("node was left waiting on init work that failed%s", log.suffix())}
		}
	}()
	return out
}

func awaitReady(results <-chan readyResult, budget time.Duration) (readyResult, bool) {
	select {
	case r := <-results:
		return r, true
	case <-time.After(budget):
		return readyResult{}, false
	}
}

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
			return &nodeReady{control: control, reader: reader, httpPort: p.HTTPPort, lifecycle: p.Lifecycle}, nil
		}
	}
}

func nodeChildEnv(sockPath string, extraEnv []string) []string {
	env := append(os.Environ(),
		"OCEL_CONTROL_SOCKET="+sockPath,
		"OCEL_HANDLER="+os.Getenv("OCEL_HANDLER"),
	)
	env = append(env, bytecode.Env()...)
	return append(env, extraEnv...)
}

func entrypointPath(a providerkit.FunctionConfig) string {
	if a.Runtime.Name == "next" {
		return "/opt/ocel/next/entrypoint.mjs"
	}
	return "/opt/ocel/node/entrypoint.mjs"
}

func nodeVersionFromBinary(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, nodeBinaryPath, "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func startNode(entrypoint string) spawner {
	return func(extraEnv []string, budget time.Duration, onControl func(io.Writer), abandon <-chan struct{}) (*nodeChild, error) {
		return spawnNode(entrypoint, extraEnv, budget, onControl, abandon)
	}
}

func spawnNode(entrypoint string, extraEnv []string, budget time.Duration, onControl func(io.Writer), abandon <-chan struct{}) (*nodeChild, error) {
	if _, err := os.Stat(entrypoint); err != nil {
		return nil, fmt.Errorf("node entrypoint not found: %w", err)
	}
	if _, err := os.Stat(nodeBinaryPath); err != nil {
		return nil, fmt.Errorf("node binary not found: %w", err)
	}

	// TODO: one path per process, so two runtimes in one sandbox cannot take each other's socket
	sockPath := "/tmp/ocel-control.sock"
	_ = os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(nodeBinaryPath, entrypoint)
	cmd.Env = nodeChildEnv(sockPath, extraEnv)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	return serveWhenReady(watchReady(ln, exited, onControl, abandon), ln, exited, budget)
}

func serveWhenReady(results <-chan readyResult, ln net.Listener, exited <-chan error, budget time.Duration) (*nodeChild, error) {
	m := &nodeChild{ready: make(chan struct{}), pending: map[string]chan struct{}{}}

	if r, shook := awaitReady(results, budget); shook {
		ln.Close()
		if r.err != nil {
			return nil, r.err
		}
		m.arm(r.ready, exited)
		return m, nil
	}

	fmt.Fprintf(os.Stderr,
		"ocel: node has not signalled ready within %s; the runtime is serving invocations that wait for it\n", budget)
	go func() {
		r := <-results
		ln.Close()
		if r.err != nil {
			fmt.Fprintf(os.Stderr, "ocel: %v\n", r.err)
			os.Exit(1)
		}
		m.arm(r.ready, exited)
	}()
	return m, nil
}

func (m *nodeChild) arm(ready *nodeReady, exited <-chan error) {
	m.upstream = upstream{port: ready.httpPort, client: newLoopbackClient()}
	m.control = ready.control
	m.lifecycle = ready.lifecycle
	if !ready.lifecycle {
		fmt.Fprintln(os.Stderr,
			"ocel: this app's entrypoint does not signal when an invocation is over, so waitUntil work is not awaited")
	}
	close(m.ready)

	go m.drainControl(ready.reader)
	go supervise("node", exited)
}

func (m *nodeChild) awaitReady(ctx context.Context) error {
	return awaitChildReady(ctx, m.ready)
}

func (m *nodeChild) awaitReadyBy(ctx context.Context, deadline time.Time) error {
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	return awaitChildReady(ctx, m.ready)
}

func (m *nodeChild) drainControl(reader *bufio.Reader) {
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var msg controlMsg
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "log":
		case "metric":
		case "request-end":
		case "invocation-complete":
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
