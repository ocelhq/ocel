package runui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/runtrace"
	"github.com/ocelhq/ocel/pkg/naming"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

var (
	environmentUnitID = naming.UnitID(naming.UnitEnvironment)
	buildStageID      = naming.PhaseID(naming.UnitEnvironment, naming.PhaseBuilding)
)

type Session struct {
	stream  *Stream
	run     *runtrace.Run
	command string
	present Presentation
	gate    gate
	waiting bool
	shown   *planv1.ChangePlan

	start      time.Time
	buildStart time.Time

	build   *lineWriter
	process *lineWriter

	logMu     sync.Mutex
	log       *os.File
	logPath   string
	logWriter *syncFileWriter
}

func New(stdout io.Writer, run *runtrace.Run, present Presentation) *Session {
	s := &Session{
		stream:  NewStream(stdout, present),
		run:     run,
		command: run.Command(),
		present: present,
		start:   time.Now(),
	}
	s.build = &lineWriter{emit: s.buildLine}
	s.process = &lineWriter{emit: s.Diagnostic}
	p := filepath.Join(run.Dir(), run.TraceID()+".log")
	if f, err := os.Create(p); err == nil {
		s.log = f
		s.logPath = p
		s.logWriter = &syncFileWriter{f: f, mu: &s.logMu}
	}
	return s
}

func (s *Session) Presentation() Presentation { return s.present }

func (s *Session) LogPath() string { return s.logPath }

func (s *Session) BuildWriter() io.Writer { return s.build }

func (s *Session) ProcessWriter() io.Writer { return s.process }

func (s *Session) buildLine(line string) {
	s.Event(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Log{
		Log: &progressv1.LogEvent{StageId: buildStageID, Message: line},
	}})
}

const maxHeldLine = 64 << 10

type lineWriter struct {
	emit func(string)

	mu   sync.Mutex
	held []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	for _, line := range w.take(p) {
		w.emit(line)
	}
	return len(p), nil
}

func (w *lineWriter) take(p []byte) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.held = append(w.held, p...)
	var ready []string
	for {
		i := bytes.IndexByte(w.held, '\n')
		if i < 0 {
			break
		}
		ready = append(ready, string(w.held[:i]))
		w.held = w.held[i+1:]
	}
	if i := bytes.LastIndexByte(w.held, '\r'); i >= 0 {
		w.held = w.held[i:]
	}
	if len(w.held) > maxHeldLine {
		ready = append(ready, string(w.held))
		w.held = nil
	}
	return ready
}

func (w *lineWriter) flush() {
	w.mu.Lock()
	line := string(w.held)
	w.held = nil
	w.mu.Unlock()
	if collapseRewrites(line) != "" {
		w.emit(line)
	}
}

func (s *Session) Suspend() func() { return s.stream.Suspend() }

func (s *Session) Diagnostic(message string) {
	s.diagnose(message, streamv1.DiagnosticLevel_DIAGNOSTIC_LEVEL_INFO)
}

func (s *Session) Warning(message string) {
	s.diagnose(message, streamv1.DiagnosticLevel_DIAGNOSTIC_LEVEL_WARNING)
}

func (s *Session) diagnose(message string, level streamv1.DiagnosticLevel) {
	s.logf("[diagnostic] %s", message)
	s.stream.Emit(&streamv1.RunEvent{Event: &streamv1.RunEvent_Diagnostic{
		Diagnostic: &streamv1.DiagnosticEvent{Message: message, Level: level},
	}})
}

func (s *Session) Plan(headline string, plan *planv1.ChangePlan, notes ...string) *planv1.ChangePlan {
	drawn, ok := proto.Clone(plan).(*planv1.ChangePlan)
	if !ok {
		return plan
	}
	drawn.Headline, drawn.Notes = headline, notes
	s.logf("[plan] %s", headline)
	s.shown = s.stream.Emit(&streamv1.RunEvent{Event: &streamv1.RunEvent_Plan{Plan: drawn}}).GetPlan()
	return s.shown
}

func (s *Session) Building() {
	s.logf("[building] Building project")
	s.buildStart = time.Now()
	s.Event(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_StagePlan{
		StagePlan: &progressv1.StagePlanEvent{Stages: []*progressv1.Stage{
			{Id: environmentUnitID, Title: "Environment"},
			{Id: buildStageID, ParentId: environmentUnitID, Title: "Building", Phase: progressv1.Phase_PHASE_BUILDING},
		}},
	}})
	s.Event(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Progress{
		Progress: &progressv1.ProgressEvent{StageId: buildStageID, Message: "Building project"},
	}})
}

func (s *Session) BuildOK() {
	if s.buildStart.IsZero() {
		return
	}
	s.build.flush()
	end := time.Now()
	s.Event(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Span{
		Span: &progressv1.SpanEvent{
			SpanId:            buildStageID,
			ParentSpanId:      environmentUnitID,
			Name:              naming.PhaseBuilding,
			StartTimeUnixNano: s.buildStart.UnixNano(),
			EndTimeUnixNano:   end.UnixNano(),
			Status:            progressv1.SpanStatus_SPAN_STATUS_OK,
		},
	}})
	s.buildStart = time.Time{}
}

func (s *Session) RestartBuild() {
	s.buildStart = time.Now()
	s.stream.Restart(buildStageID)
}

func (s *Session) Waiting(reason, url string) {
	s.logf("[waiting] %s", withoutFragment(url))
	s.waiting = true
	s.stream.Pause()
	s.stream.Emit(&streamv1.RunEvent{Event: &streamv1.RunEvent_Waiting{
		Waiting: &streamv1.WaitingEvent{Reason: reason, Url: url},
	}})
}

func (s *Session) Resume() {
	s.waiting = false
	s.stream.Emit(&streamv1.RunEvent{Event: &streamv1.RunEvent_Resumed{
		Resumed: &streamv1.ResumedEvent{Reason: "the page was answered"},
	}})
	s.stream.Resume()
}

func (s *Session) Event(ev *progressv1.OperationEvent) {
	out := s.stream.Emit(&streamv1.RunEvent{Event: &streamv1.RunEvent_Operation{Operation: ev}}).GetOperation()
	s.logOperation(out)
	if span := out.GetSpan(); span != nil {
		s.ingestSpan(span)
	}
}

func (s *Session) logOperation(ev *progressv1.OperationEvent) {
	switch {
	case ev.GetProgress() != nil:
		p := ev.GetProgress()
		s.logf("[progress] %s", progressLogLine(p.GetMessage(), p.GetCurrent(), p.Total))
	case ev.GetLog() != nil:
		s.logf("[log] %s", ev.GetLog().GetMessage())
	case ev.GetDegraded() != nil:
		s.logf("[degraded] %s: %s", ev.GetDegraded().GetNeed(), ev.GetDegraded().GetDetail())
	case ev.GetDnsOwed() != nil:
		s.logf("[dns] %s: %s", ev.GetDnsOwed().GetHeadline(), dnsLogLine(ev.GetDnsOwed().GetRecords()))
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

	s.run.IngestSpan(
		spanID, parentSpanID,
		span.GetName(),
		unixNano(span.GetStartTimeUnixNano()), unixNano(span.GetEndTimeUnixNano()),
		spanStatus(span.GetStatus()),
		spanAttributes(span.GetAttributes()),
	)
}

func (s *Session) Deployed(headline string, appURLs []string, urlNote string, flip Flip, links []*linksv1.Link, functions []*progressv1.FunctionOutput) {
	s.logOutputs(links, functions)
	s.result(&streamv1.RunResultEvent{
		Success:   true,
		Headline:  headline,
		AppUrls:   appURLs,
		UrlNote:   urlNote,
		FlipBound: flip.Bound,
	})
}

func (s *Session) Finish(headline string) {
	s.result(&streamv1.RunResultEvent{Success: true, Headline: headline})
}

func (s *Session) Fail(err error) {
	s.logf("[error] %v", err)
	s.result(&streamv1.RunResultEvent{Success: false, Detail: err.Error()})
}

func (s *Session) Cancel() {
	s.logf("[cancelled] interrupted")
	note := "Resources may be partially created."
	if s.waiting {
		note = "Nothing has been provisioned."
	}
	s.result(&streamv1.RunResultEvent{
		Interrupted: true,
		Headline:    "Cancelled",
		Detail:      fmt.Sprintf("%s\nRe-run `%s` to reconcile.", note, s.command),
	})
}

func (s *Session) result(ev *streamv1.RunResultEvent) {
	s.build.flush()
	s.process.flush()
	ev.DurationMs = time.Since(s.start).Milliseconds()
	ev.LogPath = s.logPath
	s.stream.Emit(&streamv1.RunEvent{Event: &streamv1.RunEvent_Result{Result: ev}})
}

func (s *Session) Close() error {
	s.build.flush()
	s.process.flush()
	_ = s.stream.Close()
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

func collapseRewrites(message string) string {
	if !strings.ContainsRune(message, '\r') {
		return message
	}
	lines := strings.Split(message, "\n")
	for i, line := range lines {
		drafts := strings.Split(line, "\r")
		lines[i] = ""
		for d := len(drafts) - 1; d >= 0; d-- {
			if drafts[d] != "" {
				lines[i] = drafts[d]
				break
			}
		}
	}
	return strings.Join(lines, "\n")
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
	case progressv1.AttributeKey_ATTRIBUTE_KEY_CACHED:
		return runtrace.AttrCached, true
	default:
		return "", false
	}
}
