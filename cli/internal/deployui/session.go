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

	"github.com/ocelhq/ocel/cli/internal/runtrace"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type Session struct {
	r       *Renderer
	run     *runtrace.Run
	command string
	verbose bool
	waiting bool

	logMu     sync.Mutex
	log       *os.File
	logPath   string
	logWriter *syncFileWriter
}

func New(stdout io.Writer, run *runtrace.Run, format Format, verbose bool) *Session {
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

func (s *Session) Suspend() func() { return s.r.Suspend() }

func (s *Session) Diagnostic(message string) {
	s.logf("[diagnostic] %s", message)
	if s.r.format == FormatJSON {
		s.r.emitJSON("diagnostic", map[string]any{"message": message})
		return
	}
	_, _ = fmt.Fprintln(s.r, message)
}

func (s *Session) Building() {
	s.logf("[building] Building project")
	s.r.Building()
}

func (s *Session) BuildOK() {
	s.r.BuildOK()
}

func (s *Session) RestartBuild() {
	s.r.RestartBuildStage()
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

func (s *Session) Event(ev *progressv1.OperationEvent) {
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
	if d := ev.GetDegraded(); d != nil {
		s.logf("[degraded] %s: %s", d.GetNeed(), d.GetDetail())
		s.r.Degraded(d.GetNeed(), d.GetDetail())
		return
	}
	if owed := ev.GetDnsOwed(); owed != nil {
		s.logf("[dns] %s: %s", owed.GetHeadline(), dnsLogLine(owed.GetRecords()))
		s.r.DNSOwed(owed.GetHeadline(), owed.GetRecords(), owed.GetNotes())
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

func dnsLogLine(records []*progressv1.DnsRecord) string {
	lines := make([]string, 0, len(records))
	for _, rec := range records {
		lines = append(lines, fmt.Sprintf("%s %s %s", rec.GetName(), rec.GetType(), rec.GetValue()))
	}
	return strings.Join(lines, "; ")
}

func (s *Session) ingestSpan(span *progressv1.SpanEvent) {
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
		start = s.now()
	}
	if !end.After(start) {
		end = s.now()
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

	s.r.StageEnd(spanID[:], span.GetStatus() == progressv1.SpanStatus_SPAN_STATUS_ERROR, end.Sub(start))
}

func (s *Session) now() time.Time {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	return s.r.plan.now().UTC()
}

func (s *Session) Deployed(headline string, appURLs []string, urlNote string, flip Flip, links []*linksv1.Link, functions []*progressv1.FunctionOutput) {
	s.logOutputs(links, functions)
	s.r.Deployed(headline, appURLs, urlNote, flip, s.logPath)
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

func (s *Session) logOutputs(links []*linksv1.Link, functions []*progressv1.FunctionOutput) {
	for _, l := range links {
		s.logf("[output] %s", formatLink(l))
	}
	for _, f := range functions {
		s.logf("[output] %s: %s", f.GetLogicalName(), f.GetUrl())
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

func formatLink(l *linksv1.Link) string {
	switch p := l.GetProperties().(type) {
	case *linksv1.Link_Postgres:
		return fmt.Sprintf("%s: postgres://%s@%s:%d/%s", l.GetName(), p.Postgres.GetUsername(), p.Postgres.GetHost(), p.Postgres.GetPort(), p.Postgres.GetDatabase())
	case *linksv1.Link_Bucket:
		return fmt.Sprintf("%s: bucket %s", l.GetName(), p.Bucket.GetBucket())
	}
	return l.GetName()
}

func spanStatus(s progressv1.SpanStatus) runtrace.SpanStatus {
	switch s {
	case progressv1.SpanStatus_SPAN_STATUS_OK:
		return runtrace.SpanStatusOK
	case progressv1.SpanStatus_SPAN_STATUS_ERROR:
		return runtrace.SpanStatusError
	default:
		return runtrace.SpanStatusUnset
	}
}

var numericAttributeKeys = map[attribute.Key]struct{}{
	runtrace.AttrExitCode:      {},
	runtrace.AttrResourceCount: {},
	runtrace.AttrBytes:         {},
	runtrace.AttrRetryCount:    {},
	runtrace.AttrDurationMS:    {},
}

func spanAttributes(attrs []*progressv1.SpanAttribute) []attribute.KeyValue {
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

func attributeKey(k progressv1.AttributeKey) (attribute.Key, bool) {
	switch k {
	case progressv1.AttributeKey_ATTRIBUTE_KEY_COMMAND:
		return runtrace.AttrCommand, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_STAGE:
		return runtrace.AttrStage, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_APP:
		return runtrace.AttrApp, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_PHASE:
		return runtrace.AttrPhase, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_PROVIDER:
		return runtrace.AttrProvider, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_EXIT_CODE:
		return runtrace.AttrExitCode, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_ERROR_KIND:
		return runtrace.AttrErrorKind, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_COUNT:
		return runtrace.AttrResourceCount, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_BYTES:
		return runtrace.AttrBytes, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_RETRY_COUNT:
		return runtrace.AttrRetryCount, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_DURATION_MS:
		return runtrace.AttrDurationMS, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE:
		return runtrace.AttrResourceType, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME:
		return runtrace.AttrResourceName, true
	default:
		return "", false
	}
}
