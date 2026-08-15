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

const completionMargin = 500 * time.Millisecond

const startupBudget = 8 * time.Second

const minSpawnBudget = 4 * time.Second

const nodeBinaryPath = "/var/lang/bin/node"

type nodeChild struct {
	nodePort int
	control  net.Conn
	client   *http.Client

	live *liveValues

	bytecode *bytecodeUpload

	bytecodeSource bytecodeSource

	bytecodeKey string

	lifecycle bool

	mu           sync.Mutex
	pending      map[string]chan struct{}
	flushWaiter  chan compileCacheFlushedPayload
	warmWaiter   chan compileCacheWarmedPayload
	bytecodeDone bool
}

func (m *nodeChild) bytecodeCacheSource() bytecodeSource {
	if m.bytecodeSource == "" {
		return bytecodeSourceNone
	}
	return m.bytecodeSource
}

func (m *nodeChild) bytecodeCached() bool {
	return m.bytecodeCacheSource() != bytecodeSourceNone
}

func (m *nodeChild) registerWaiter(requestID string) <-chan struct{} {
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
	m.control.SetWriteDeadline(time.Time{})
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

func startNode(extraEnv []string, budget time.Duration, onControl func(io.Writer), abandon <-chan struct{}) (*nodeChild, error) {
	// TODO: randomize
	sockPath := "/tmp/ocel-control.sock"
	_ = os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(nodeBinaryPath, entrypointPath())
	cmd.Env = nodeChildEnv(sockPath, extraEnv)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	ready, err := awaitReady(ln, exited, budget, onControl, abandon)
	ln.Close()
	if err != nil {
		return nil, err
	}

	m := &nodeChild{
		control:   ready.control,
		nodePort:  ready.httpPort,
		lifecycle: ready.lifecycle,
		pending:   map[string]chan struct{}{},
		client:    newLoopbackClient(),
	}
	if !ready.lifecycle {
		fmt.Fprintln(os.Stderr,
			"ocel: this app's entrypoint does not signal when an invocation is over, so waitUntil work is not awaited")
	}

	go m.drainControl(ready.reader)
	go superviseNode(exited)
	return m, nil
}

func newLoopbackClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			MaxIdleConns:        16,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     4 * time.Second,
		},
	}
}

func superviseNode(exited <-chan error) {
	err := <-exited
	fmt.Fprintf(os.Stderr, "ocel: node exited after startup: %v\n", err)
	os.Exit(1)
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
