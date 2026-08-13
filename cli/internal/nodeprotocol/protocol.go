// Package nodeprotocol reads the newline-delimited protocol the Node
// subprocesses (the builder and the discovery entrypoint) write to their
// own stdout, and converts it into obs spans and log records. Anything on
// that stream that isn't a protocol record — a framework's own build
// output, in particular — is forwarded untouched.
package nodeprotocol

import (
	"bufio"
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

// Prefix marks a line as a protocol record rather than free-form output
// from the framework being built or from user discovery code. It is
// deliberately a literal a human or a bundler is exceedingly unlikely to
// print by coincidence, and it is checked with a plain prefix compare
// before anything is handed to json.Unmarshal, so ordinary JSON a
// framework prints to stdout is never mistaken for one of these records.
const Prefix = "@@OCEL_V1@@"

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
}

// Processor turns one subprocess's protocol stream into spans and log
// records on Run, forwarding everything else to Forward. Run may be nil,
// in which case records are parsed (so forwarding still behaves the same)
// but nothing is recorded — callers that have no active obs.Run still get
// correct passthrough.
type Processor struct {
	Run     *obs.Run
	Forward io.Writer

	mu    sync.Mutex
	spans map[string]openSpan
	err   string
}

// Scan reads r line by line until EOF, applying each line. It does not
// return an error: a read error simply ends the scan, matching the
// behavior of a subprocess that closed its stdout.
func (p *Processor) Scan(ctx context.Context, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		p.line(ctx, scanner.Text())
	}
}

func (p *Processor) line(ctx context.Context, line string) {
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
	p.apply(ctx, rec)
}

func (p *Processor) forward(line string) {
	if p.Forward == nil {
		return
	}
	_, _ = io.WriteString(p.Forward, line+"\n")
}

func (p *Processor) apply(ctx context.Context, rec record) {
	switch rec.Type {
	case typeLog:
		if p.Run != nil {
			p.Run.Log(ctx, obs.Level(rec.Level), obs.Stage(rec.Stage), obs.App(rec.App), rec.Message)
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
			p.Run.Error(ctx, obs.Stage(rec.Stage), obs.App(rec.App), rec.Message)
		}
	}
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
	p.spans[rec.ID] = openSpan{ctx: spanCtx, span: span}
	p.mu.Unlock()
}

func (p *Processor) endSpan(rec record) {
	p.mu.Lock()
	s, found := p.spans[rec.ID]
	if found {
		delete(p.spans, rec.ID)
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

// Abort ends any span that never received a matching span_end — the
// process that would have sent it exited first, most often because it
// crashed mid-span. Ending it as failed keeps a crash from leaving an
// unterminated span out of the trace file entirely.
func (p *Processor) Abort() {
	p.mu.Lock()
	spans := p.spans
	p.spans = nil
	p.mu.Unlock()
	for _, s := range spans {
		s.span.SetStatus(codes.Error, "")
		s.span.End()
	}
}

// Err returns the message of the last error record seen, or "" if none
// was. This is the actual failure the subprocess reported, as opposed to
// a heuristic guess at which trailing output line mattered.
func (p *Processor) Err() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}
