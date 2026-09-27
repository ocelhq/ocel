package nodeprotocol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/cli/internal/runtrace"
)

const Prefix = "@@OCEL_V1@@"

const maxLineBytes = 4 * 1024 * 1024

var validStages = map[string]bool{
	buildStage:  true,
	"discovery": true,
}

const maxAppLen = 128

type syncWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (s syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// SyncPair wraps a and b, which may or may not be the same underlying
// writer, so that concurrent writes through either one are mutually
// exclusive. A subprocess's own stderr-draining goroutine and the goroutine
// running Scan can otherwise write to what turns out to be one writer at
// once.
func SyncPair(a, b io.Writer) (io.Writer, io.Writer) {
	mu := &sync.Mutex{}
	wrap := func(w io.Writer) io.Writer {
		if w == nil {
			return nil
		}
		return syncWriter{mu, w}
	}
	return wrap(a), wrap(b)
}

type recordType string

const (
	typeLog       recordType = "log"
	typeSpanStart recordType = "span_start"
	typeSpanEnd   recordType = "span_end"
	typeError     recordType = "error"
)

type record struct {
	Type    recordType `json:"type"`
	App     string     `json:"app,omitempty"`
	Stage   string     `json:"stage,omitempty"`
	Level   string     `json:"level,omitempty"`
	Message string     `json:"message,omitempty"`
	ID      string     `json:"id,omitempty"`
	OK      *bool      `json:"ok,omitempty"`
}

type openSpan struct {
	ctx  context.Context
	span trace.Span
	app  string
}

type Processor struct {
	Run      *runtrace.Run
	Forward  io.Writer
	AppBuild func(app string) (ended func(error))

	mu           sync.Mutex
	spans        map[string]openSpan
	spanCtxByApp map[string]context.Context
	builds       map[string]func(error)
	err          string
	unread       error
}

const buildStage = "build"

var errBuilderExited = errors.New("the node builder exited before this app's build ended")

var errBuildCancelled = errors.New("cancelled before this app's build ended")

var errBuildFailed = errors.New("the node builder reported this app's build failed")

func (p *Processor) Scan(ctx context.Context, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes+64*1024)
	scanner.Split(splitLines)
	for scanner.Scan() {
		p.line(ctx, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		p.mu.Lock()
		p.unread = fmt.Errorf("could not read the node builder's output: %w", err)
		p.mu.Unlock()
		_, _ = io.Copy(p.forwardWriter(), r)
	}
}

func splitLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		if len(data) == 0 {
			return 0, nil, nil
		}
		return len(data), data, nil
	}
	if len(data) >= maxLineBytes {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func (p *Processor) forwardWriter() io.Writer {
	if p.Forward == nil {
		return io.Discard
	}
	return p.Forward
}

func (p *Processor) line(ctx context.Context, line string) {
	if len(line) >= maxLineBytes {
		p.forward(line)
		return
	}
	payload, ok := strings.CutPrefix(line, Prefix)
	if !ok {
		p.forward(line)
		return
	}
	var rec record
	if err := json.Unmarshal([]byte(payload), &rec); err != nil {
		p.forward(line)
		return
	}
	if rec.Stage != "" && !validStages[rec.Stage] {
		p.forward(line)
		return
	}
	if len(rec.App) > maxAppLen {
		rec.App = rec.App[:maxAppLen]
	}
	p.apply(ctx, rec)
}

func (p *Processor) forward(line string) {
	// TODO: this re-emits a trailing \n on every token, so CRLF fidelity
	// and byte-for-byte streaming are lost (a bare-\r spinner now flushes
	// only at the next real newline), and a final partial line gets a \n
	// the source never wrote. Fixing it needs Scan to thread raw bytes
	// rather than bufio.Scanner tokens.
	if p.Forward == nil {
		return
	}
	_, _ = io.WriteString(p.Forward, line+"\n")
}

func (p *Processor) apply(ctx context.Context, rec record) {
	switch rec.Type {
	case typeLog:
		if p.Run != nil {
			p.Run.Log(p.logContext(ctx, rec.App), rec.App, rec.Message)
		}
	case typeSpanStart:
		p.startBuild(rec)
		p.startSpan(ctx, rec)
	case typeSpanEnd:
		p.endBuild(rec)
		p.endSpan(rec)
	case typeError:
		p.mu.Lock()
		p.err = rec.Message
		p.mu.Unlock()
		if p.Run != nil {
			p.Run.Log(p.logContext(ctx, rec.App), rec.App, rec.Message)
		}
	}
}

func (p *Processor) logContext(ctx context.Context, app string) context.Context {
	if app == "" {
		return ctx
	}
	p.mu.Lock()
	spanCtx, found := p.spanCtxByApp[app]
	p.mu.Unlock()
	if !found {
		return ctx
	}
	return spanCtx
}

func (p *Processor) startBuild(rec record) {
	if p.AppBuild == nil || rec.Stage != buildStage || rec.App == "" || rec.ID == "" {
		return
	}
	ended := p.AppBuild(rec.App)
	p.mu.Lock()
	if p.builds == nil {
		p.builds = make(map[string]func(error))
	}
	p.builds[rec.ID] = ended
	p.mu.Unlock()
}

func (p *Processor) endBuild(rec record) {
	p.mu.Lock()
	ended, found := p.builds[rec.ID]
	delete(p.builds, rec.ID)
	failure := p.err
	p.mu.Unlock()
	if !found {
		return
	}
	switch {
	case rec.OK == nil || *rec.OK:
		ended(nil)
	case failure != "":
		first, _, _ := strings.Cut(failure, "\n")
		ended(errors.New(first))
	default:
		ended(errBuildFailed)
	}
}

func (p *Processor) startSpan(ctx context.Context, rec record) {
	if p.Run == nil || rec.ID == "" {
		return
	}
	var attrs []attribute.KeyValue
	if rec.App != "" {
		attrs = append(attrs, runtrace.AttrApp.String(rec.App))
	}
	spanCtx, span := p.Run.StartSpan(ctx, rec.Stage, attrs...)

	p.mu.Lock()
	if p.spans == nil {
		p.spans = make(map[string]openSpan)
	}
	p.spans[rec.ID] = openSpan{ctx: spanCtx, span: span, app: rec.App}
	if rec.App != "" {
		if p.spanCtxByApp == nil {
			p.spanCtxByApp = make(map[string]context.Context)
		}
		p.spanCtxByApp[rec.App] = spanCtx
	}
	p.mu.Unlock()
}

func (p *Processor) endSpan(rec record) {
	p.mu.Lock()
	s, found := p.spans[rec.ID]
	if found {
		delete(p.spans, rec.ID)
		if s.app != "" && p.spanCtxByApp[s.app] == s.ctx {
			delete(p.spanCtxByApp, s.app)
		}
	}
	p.mu.Unlock()
	if !found {
		return
	}
	if rec.OK != nil && !*rec.OK {
		s.span.SetStatus(codes.Error, "")
	}
	s.span.End()
}

func (p *Processor) Abort(ctx context.Context) error {
	p.mu.Lock()
	spans := p.spans
	builds := p.builds
	cause := p.unread
	p.spans = nil
	p.spanCtxByApp = nil
	p.builds = nil
	p.mu.Unlock()
	for _, s := range spans {
		s.span.SetStatus(codes.Error, "")
		s.span.End()
	}
	if len(builds) == 0 {
		return nil
	}
	switch {
	case ctx.Err() != nil:
		cause = errBuildCancelled
	case cause == nil:
		cause = errBuilderExited
	}
	for _, ended := range builds {
		ended(cause)
	}
	return cause
}

func (p *Processor) Failure() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}
