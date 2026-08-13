package deployui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/ocelhq/ocel/cli/internal/obs"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

type Session struct {
	r       *Renderer
	run     *obs.Run
	command string
	verbose bool
	waiting bool

	logMu     sync.Mutex
	log       *os.File
	logPath   string
	logWriter *syncFileWriter
}

func New(stdout io.Writer, run *obs.Run, format Format, verbose bool) *Session {
	s := &Session{
		r:       NewRenderer(stdout, format, verbose),
		run:     run,
		command: run.Command(),
		verbose: verbose,
	}
	p := filepath.Join(run.Dir(), run.TraceID()+".log")
	if f, err := os.Create(p); err == nil {
		s.log = f
		s.logPath = p
		s.logWriter = &syncFileWriter{f: f, mu: &s.logMu}
	}
	return s
}

func (s *Session) LogPath() string {
	return s.logPath
}

func (s *Session) BuildWriter() io.Writer {
	if s.r.Live() || !s.verbose {
		if s.logWriter != nil {
			return s.logWriter
		}
		return io.Discard
	}
	if s.logWriter != nil {
		return io.MultiWriter(s.r, s.logWriter)
	}
	return s.r
}

func (s *Session) Building() {
	s.logf("[building] Building project")
	s.r.Building()
}

func (s *Session) BuildOK() {
	s.r.BuildOK()
}

func (s *Session) Waiting(reason, url string) {
	s.logf("[waiting] %s", withoutFragment(url))
	s.waiting = true
	s.r.Waiting(reason, url)
}

func (s *Session) Resume() {
	s.waiting = false
	s.r.Resume()
}

func (s *Session) Event(ev *deploymentsv1.DeployEvent) {
	if p := ev.GetProgress(); p != nil {
		s.logf("[%s] %s", phaseTag(p.GetPhase()), progressLogLine(p.GetMessage(), p.GetCurrent(), p.Total))
		s.r.Progress(p.GetStageId(), p.GetPhase(), p.GetMessage(), p.GetCurrent(), p.Total)
		return
	}
	if l := ev.GetLog(); l != nil {
		s.logf("[log] %s", l.GetMessage())
		s.r.Log(l.GetMessage())
		return
	}
	if sp := ev.GetStagePlan(); sp != nil {
		s.r.StagePlan(sp)
		return
	}
	if span := ev.GetSpan(); span != nil {
		s.ingestSpan(span)
		return
	}
}

func (s *Session) ingestSpan(span *deploymentsv1.SpanEvent) {
	var spanID, parentSpanID [8]byte
	if id := span.GetSpanId(); len(id) == 8 {
		copy(spanID[:], id)
	} else {
		s.logf("[warn] dropped a provider span with a malformed span id (%d bytes, want 8)", len(span.GetSpanId()))
		return
	}
	if id := span.GetParentSpanId(); len(id) == 8 {
		copy(parentSpanID[:], id)
	}

	start := unixNano(span.GetStartTimeUnixNano())
	end := unixNano(span.GetEndTimeUnixNano())
	if start.IsZero() {
		start = time.Now().UTC()
	}
	if end.Before(start) {
		end = start
	}

	s.run.IngestSpan(
		spanID, parentSpanID,
		span.GetName(),
		start, end,
		spanStatus(span.GetStatus()),
		spanAttributes(span.GetAttributes()),
	)
}

func (s *Session) Deployed(headline string, appURLs []string, outputs []*deploymentsv1.ResourceOutput) {
	s.logOutputs(outputs)
	s.r.Deployed(headline, appURLs, s.logPath)
}

func (s *Session) Finish(headline string) {
	s.r.Finish(headline, s.logPath)
}

func (s *Session) Fail(err error) {
	s.logf("[error] %v", err)
	s.r.Fail(err, s.logPath)
}

func (s *Session) Cancel() {
	s.logf("[cancelled] interrupted")
	s.r.Cancel(s.command, s.waiting, s.logPath)
}

func (s *Session) Close() error {
	_ = s.r.Close()
	if s.log != nil {
		return s.log.Close()
	}
	return nil
}

func (s *Session) logf(format string, args ...any) {
	if s.logWriter == nil {
		return
	}
	_, _ = s.logWriter.Write([]byte(fmt.Sprintf(format, args...) + "\n"))
}

func (s *Session) logOutputs(outputs []*deploymentsv1.ResourceOutput) {
	for _, o := range outputs {
		s.logf("[output] %s", formatOutput(o))
	}
}

type syncFileWriter struct {
	f  *os.File
	mu *sync.Mutex
}

func (w *syncFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Write(p)
}

func withoutFragment(url string) string {
	if i := strings.Index(url, "#"); i >= 0 {
		return url[:i]
	}
	return url
}

func progressLogLine(message string, current uint32, total *uint32) string {
	if total != nil {
		return fmt.Sprintf("%s (%d/%d)", message, current, *total)
	}
	return message
}

func unixNano(ns int64) time.Time {
	if ns <= 0 {
		return time.Time{}
	}
	return time.Unix(0, ns).UTC()
}

func relLog(logPath string) string {
	if logPath == "" {
		return ""
	}
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, logPath); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return logPath
}

func formatOutput(o *deploymentsv1.ResourceOutput) string {
	if pg := o.GetPostgres(); pg != nil {
		return fmt.Sprintf("%s: postgres://%s@%s:%d/%s", o.GetLogicalName(), pg.GetUsername(), pg.GetHost(), pg.GetPort(), pg.GetDatabase())
	}
	if b := o.GetBucket(); b != nil {
		return fmt.Sprintf("%s: bucket %s at %s", o.GetLogicalName(), b.GetBucket(), b.GetAddress())
	}
	if f := o.GetFunction(); f != nil {
		return fmt.Sprintf("%s: %s", o.GetLogicalName(), f.GetUrl())
	}
	return o.GetLogicalName()
}

func spanStatus(s deploymentsv1.SpanStatus) obs.SpanStatus {
	switch s {
	case deploymentsv1.SpanStatus_SPAN_STATUS_OK:
		return obs.SpanStatusOK
	case deploymentsv1.SpanStatus_SPAN_STATUS_ERROR:
		return obs.SpanStatusError
	default:
		return obs.SpanStatusUnset
	}
}

var numericAttributeKeys = map[attribute.Key]struct{}{
	obs.AttrExitCode:      {},
	obs.AttrResourceCount: {},
	obs.AttrBytes:         {},
	obs.AttrRetryCount:    {},
	obs.AttrDurationMS:    {},
}

func spanAttributes(attrs []*deploymentsv1.SpanAttribute) []attribute.KeyValue {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		key, ok := attributeKey(a.GetKey())
		if !ok {
			continue
		}
		out = append(out, attributeValue(key, a.GetValue()))
	}
	return out
}

func attributeValue(key attribute.Key, value string) attribute.KeyValue {
	if _, numeric := numericAttributeKeys[key]; numeric {
		if n, err := strconv.ParseInt(value, 10, 64); err == nil {
			return key.Int64(n)
		}
	}
	return key.String(value)
}

func attributeKey(k deploymentsv1.AttributeKey) (attribute.Key, bool) {
	switch k {
	case deploymentsv1.AttributeKey_ATTRIBUTE_KEY_COMMAND:
		return obs.AttrCommand, true
	case deploymentsv1.AttributeKey_ATTRIBUTE_KEY_STAGE:
		return obs.AttrStage, true
	case deploymentsv1.AttributeKey_ATTRIBUTE_KEY_APP:
		return obs.AttrApp, true
	case deploymentsv1.AttributeKey_ATTRIBUTE_KEY_PHASE:
		return obs.AttrPhase, true
	case deploymentsv1.AttributeKey_ATTRIBUTE_KEY_PROVIDER:
		return obs.AttrProvider, true
	case deploymentsv1.AttributeKey_ATTRIBUTE_KEY_EXIT_CODE:
		return obs.AttrExitCode, true
	case deploymentsv1.AttributeKey_ATTRIBUTE_KEY_ERROR_KIND:
		return obs.AttrErrorKind, true
	case deploymentsv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_COUNT:
		return obs.AttrResourceCount, true
	case deploymentsv1.AttributeKey_ATTRIBUTE_KEY_BYTES:
		return obs.AttrBytes, true
	case deploymentsv1.AttributeKey_ATTRIBUTE_KEY_RETRY_COUNT:
		return obs.AttrRetryCount, true
	case deploymentsv1.AttributeKey_ATTRIBUTE_KEY_DURATION_MS:
		return obs.AttrDurationMS, true
	case deploymentsv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE:
		return obs.AttrResourceType, true
	case deploymentsv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME:
		return obs.AttrResourceName, true
	default:
		return "", false
	}
}
