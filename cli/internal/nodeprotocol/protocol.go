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

	"github.com/ocelhq/ocel/cli/internal/redaction"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/progress"
)

const Prefix = "@@OCEL_V1@@"

const maxLineBytes = 4 * 1024 * 1024

var validStages = map[string]bool{
	buildStage:  true,
	"discovery": true,
}

const maxAppBytes = 128

type syncWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (s syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

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
	span *run.Span
	app  string
}

type Processor struct {
	Span     *run.Span
	Forward  io.Writer
	AppBuild func(app string) (ended func(error))
	Hide     redaction.Values

	forwarding *redaction.Writer

	mu        sync.Mutex
	spans     map[string]openSpan
	spanByApp map[string]*run.Span
	builds    map[string]func(error)
	err       string
	unread    error
}

const buildStage = "build"

var errBuilderExited = errors.New("the node builder exited before this app's build ended")

var errBuildCancelled = errors.New("cancelled before this app's build ended")

var errBuildFailed = errors.New("the node builder reported this app's build failed")

var errStageFailed = errors.New("the node builder reported this stage failed")

var errStageAbandoned = errors.New("the node builder never ended this stage")

func (p *Processor) Scan(ctx context.Context, r io.Reader) {
	defer func() { _ = p.forwarded().Flush() }()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes+64*1024)
	scanner.Split(splitLines)
	for scanner.Scan() {
		p.line(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		p.mu.Lock()
		p.unread = fmt.Errorf("could not read the node builder's output: %w", err)
		p.mu.Unlock()
		_, _ = io.Copy(p.forwarded(), r)
	}
}

func splitLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i+1], nil
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

func (p *Processor) hide(text string) string {
	return p.Hide.Hide(text)
}

func (p *Processor) forwarded() *redaction.Writer {
	if p.forwarding == nil {
		forward := p.Forward
		if forward == nil {
			forward = io.Discard
		}
		p.forwarding = p.Hide.Writer(forward)
	}
	return p.forwarding
}

func (p *Processor) line(token string) {
	line, terminated := strings.CutSuffix(token, "\n")
	if len(line) >= maxLineBytes {
		if !terminated {
			_, _ = io.WriteString(p.forwarded(), line)
			return
		}
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
	if len(rec.App) > maxAppBytes {
		rec.App = rec.App[:maxAppBytes]
	}
	rec.Message = p.hide(rec.Message)
	p.apply(rec)
}

func (p *Processor) forward(line string) {
	// TODO: this re-emits a trailing \n on every line it holds whole, so CRLF fidelity
	// and byte-for-byte streaming are lost (a bare-\r spinner now flushes
	// only at the next real newline), and a final partial line gets a \n
	// the source never wrote. Fixing it needs Scan to thread raw bytes
	// rather than bufio.Scanner tokens.
	_, _ = io.WriteString(p.forwarded(), line+"\n")
}

func (p *Processor) apply(rec record) {
	switch rec.Type {
	case typeLog:
		p.log(rec)
	case typeSpanStart:
		p.startBuild(rec)
		p.startSpan(rec)
	case typeSpanEnd:
		p.endBuild(rec)
		p.endSpan(rec)
	case typeError:
		p.mu.Lock()
		p.err = rec.Message
		p.mu.Unlock()
		p.log(rec)
	}
}

func (p *Processor) log(rec record) {
	p.mu.Lock()
	span, found := p.spanByApp[rec.App]
	p.mu.Unlock()
	switch {
	case found:
		span.Debug(rec.Message)
	case p.Span != nil:
		p.Span.Debug(rec.Message)
	}
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

func (p *Processor) startSpan(rec record) {
	if p.Span == nil || rec.ID == "" {
		return
	}
	var attrs []progress.Attr
	if rec.App != "" {
		attrs = append(attrs, progress.Attr{Key: progress.AttrKeyApp, Value: rec.App})
	}
	span := p.Span.Trace(rec.App, rec.Stage, attrs...)

	p.mu.Lock()
	if p.spans == nil {
		p.spans = make(map[string]openSpan)
	}
	p.spans[rec.ID] = openSpan{span: span, app: rec.App}
	if rec.App != "" {
		if p.spanByApp == nil {
			p.spanByApp = make(map[string]*run.Span)
		}
		p.spanByApp[rec.App] = span
	}
	p.mu.Unlock()
}

func (p *Processor) endSpan(rec record) {
	p.mu.Lock()
	s, found := p.spans[rec.ID]
	if found {
		delete(p.spans, rec.ID)
		if s.app != "" && p.spanByApp[s.app] == s.span {
			delete(p.spanByApp, s.app)
		}
	}
	p.mu.Unlock()
	if !found {
		return
	}
	if rec.OK != nil && !*rec.OK {
		s.span.End(errStageFailed)
		return
	}
	s.span.End(nil)
}

func (p *Processor) Abort(ctx context.Context) error {
	p.mu.Lock()
	spans := p.spans
	builds := p.builds
	cause := p.unread
	p.spans = nil
	p.spanByApp = nil
	p.builds = nil
	p.mu.Unlock()
	for _, s := range spans {
		s.span.End(errStageAbandoned)
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
