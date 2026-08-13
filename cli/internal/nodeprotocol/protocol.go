// Package nodeprotocol reads the newline-delimited protocol the Node
// subprocesses (the builder and the discovery entrypoint) write to their
// own stdout, and converts it into obs spans and log records. Anything on
// that stream that isn't a protocol record — a framework's own build
// output, in particular — is forwarded untouched.
package nodeprotocol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/cli/internal/obs"
)

const Prefix = "@@OCEL_V1@@"

const maxLineBytes = 4 * 1024 * 1024

var validStages = map[string]bool{
	"build":     true,
	"discovery": true,
}

const maxAppLen = 128

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
	Run     *obs.Run
	Forward io.Writer

	mu           sync.Mutex
	spans        map[string]openSpan
	spanCtxByApp map[string]context.Context
	err          string
}

func (p *Processor) Scan(ctx context.Context, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes+64*1024)
	scanner.Split(splitLines)
	for scanner.Scan() {
		p.line(ctx, scanner.Text())
	}
	if scanner.Err() != nil {
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
			p.Run.Log(p.logContext(ctx, rec.App), obs.Level(rec.Level), obs.Stage(rec.Stage), obs.App(rec.App), rec.Message)
		}
	case typeSpanStart:
		p.startSpan(ctx, rec)
	case typeSpanEnd:
		p.endSpan(rec)
	case typeError:
		p.mu.Lock()
		p.err = rec.Message
		p.mu.Unlock()
		if p.Run != nil {
			p.Run.Error(p.logContext(ctx, rec.App), obs.Stage(rec.Stage), obs.App(rec.App), rec.Message)
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

func (p *Processor) startSpan(ctx context.Context, rec record) {
	if p.Run == nil || rec.ID == "" {
		return
	}
	var attrs []attribute.KeyValue
	if rec.App != "" {
		attrs = append(attrs, obs.AttrApp.String(rec.App))
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

func (p *Processor) Abort() {
	p.mu.Lock()
	spans := p.spans
	p.spans = nil
	p.spanCtxByApp = nil
	p.mu.Unlock()
	for _, s := range spans {
		s.span.SetStatus(codes.Error, "")
		s.span.End()
	}
}

func (p *Processor) Err() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}
