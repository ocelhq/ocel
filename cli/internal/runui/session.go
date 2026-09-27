package runui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
	"github.com/ocelhq/ocel/pkg/naming"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

var (
	environmentUnitID = naming.UnitID(naming.UnitEnvironment)
	buildStageID      = naming.PhaseID(naming.UnitEnvironment, naming.PhaseBuilding)
	provisionStageID  = naming.PhaseID(naming.UnitEnvironment, naming.PhaseProvisioning)
)

type Session struct {
	bus     *events.Bus
	human   *HumanSink
	run     *runtrace.Run
	command string
	present Presentation
	gate    consent.Gate
	waiting bool
	shown   *planv1.ChangePlan
	apps    []*progressv1.AppResult

	start        time.Time
	buildStart   time.Time
	buildAttempt int

	build   *lineWriter
	process sync.Map

	closeOnce sync.Once
}

var liveSessions sync.Map

func Interrupt() {
	liveSessions.Range(func(k, _ any) bool {
		if s, ok := k.(*Session); ok {
			s.interrupt()
		}
		return true
	})
}

func New(stdout io.Writer, run *runtrace.Run, present Presentation) *Session {
	s := &Session{
		bus:     events.NewBus(time.Now),
		run:     run,
		command: run.Command(),
		present: present,
		start:   time.Now(),
	}
	if present.Format == FormatJSON {
		s.bus.Attach(NewJSONSink(stdout))
	} else {
		s.human = NewHumanSink(stdout, present)
		s.bus.Attach(s.human)
	}
	s.bus.Attach(run)
	s.build = &lineWriter{emit: s.buildLine}
	liveSessions.Store(s, struct{}{})
	return s
}

func (s *Session) Presentation() Presentation { return s.present }

func (s *Session) LogPath() string { return s.run.LogPath() }

func (s *Session) BuildWriter() io.Writer { return s.build }

func (s *Session) ProcessWriter(provider string, stream progressv1.Stream) io.Writer {
	w := &lineWriter{emit: func(line string) { s.processLine(provider, stream, line) }}
	s.process.Store(stream, w)
	return w
}

func (s *Session) processLine(provider string, stream progressv1.Stream, line string) {
	s.emit(&streamv1.RunEvent{
		Level:   progressv1.Level_LEVEL_DEBUG,
		Subject: provider,
		Message: line,
		Body:    &streamv1.RunEvent_Output{Output: &progressv1.Output{Stream: stream}},
	})
}

func (s *Session) flushLines() {
	s.build.flush()
	s.process.Range(func(_, w any) bool {
		w.(*lineWriter).flush()
		return true
	})
}

func (s *Session) buildLine(line string) {
	s.record(&streamv1.RunEvent{
		Phase:   progressv1.Phase_PHASE_BUILD,
		SpanId:  buildStageID,
		Message: line,
		Body:    &streamv1.RunEvent_Output{Output: &progressv1.Output{}},
	})
}

const maxBufferedLine = 64 << 10

type lineWriter struct {
	emit func(string)

	mu      sync.Mutex
	pending []byte
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
	w.pending = append(w.pending, p...)
	var ready []string
	for {
		i := bytes.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		line := string(w.pending[:i])
		w.pending = w.pending[i+1:]
		if collapseRewrites(line) != "" {
			ready = append(ready, line)
		}
	}
	if i := bytes.LastIndexByte(w.pending, '\r'); i >= 0 {
		w.pending = w.pending[i:]
	}
	if len(w.pending) > maxBufferedLine {
		ready = append(ready, string(w.pending))
		w.pending = nil
	}
	return ready
}

func (w *lineWriter) flush() {
	w.mu.Lock()
	line := string(w.pending)
	w.pending = nil
	w.mu.Unlock()
	if collapseRewrites(line) != "" {
		w.emit(line)
	}
}

func (s *Session) Suspend() func() {
	if s.human == nil {
		return func() {}
	}
	return s.human.Suspend()
}

func (s *Session) Spin(message string) *Spinner {
	if s.human == nil {
		return &Spinner{}
	}
	return s.human.Spin(message)
}

func (s *Session) emit(ev *streamv1.RunEvent) *streamv1.RunEvent {
	return s.bus.Send(normalize(ev))
}

func (s *Session) Diagnostic(message string) {
	s.say(message, progressv1.Level_LEVEL_INFO)
}

func (s *Session) Warning(message string) {
	s.say(message, progressv1.Level_LEVEL_WARN)
}

func (s *Session) say(message string, level progressv1.Level) {
	s.emit(&streamv1.RunEvent{Level: level, Message: message})
}

func (s *Session) Identity(ev *streamv1.IdentityEvent) {
	s.emit(&streamv1.RunEvent{Body: &streamv1.RunEvent_Identity{Identity: ev}})
}

func (s *Session) Plan(headline string, plan *planv1.ChangePlan, notes ...string) *planv1.ChangePlan {
	drawn := proto.Clone(plan).(*planv1.ChangePlan)
	drawn.Headline, drawn.Notes = headline, notes
	s.shown = proto.CloneOf(s.emit(&streamv1.RunEvent{Phase: progressv1.Phase_PHASE_PLAN, Body: &streamv1.RunEvent_Plan{Plan: drawn}}).GetPlan())
	return s.shown
}

func (s *Session) Building() {
	s.buildStart = time.Now()
	s.started(environmentUnitID, nil, progressv1.Phase_PHASE_BUILD, "Environment")
	s.started(buildStageID, environmentUnitID, progressv1.Phase_PHASE_BUILD, "Building")
	s.started(provisionStageID, environmentUnitID, progressv1.Phase_PHASE_PROVISION, "Provisioning")
	s.record(&streamv1.RunEvent{Phase: progressv1.Phase_PHASE_BUILD, SpanId: buildStageID, Message: "Building project"})
}

func (s *Session) started(id, parentID []byte, phase progressv1.Phase, title string) {
	s.record(&streamv1.RunEvent{
		Phase:   phase,
		SpanId:  id,
		Message: title,
		Body:    &streamv1.RunEvent_Started{Started: &progressv1.Started{ParentSpanId: parentID}},
	})
}

func (s *Session) BuildOK() {
	if s.buildStart.IsZero() {
		return
	}
	s.build.flush()
	ended := &progressv1.Ended{
		Status:            progressv1.SpanStatus_SPAN_STATUS_OK,
		StartTimeUnixNano: s.buildStart.UnixNano(),
	}
	if s.buildAttempt > 0 {
		ended.Attributes = []*progressv1.SpanAttribute{{
			Key:   progressv1.AttributeKey_ATTRIBUTE_KEY_RETRY_COUNT,
			Value: strconv.Itoa(s.buildAttempt),
		}}
	}
	s.record(&streamv1.RunEvent{
		Time:   timestamppb.Now(),
		Phase:  progressv1.Phase_PHASE_BUILD,
		SpanId: buildStageID,
		Body:   &streamv1.RunEvent_Ended{Ended: ended},
	})
	s.buildStart = time.Time{}
}

func (s *Session) Waiting(missing *streamv1.MissingVariables, url string) {
	s.waiting = true
	s.build.flush()
	s.buildStart = time.Time{}
	s.emit(&streamv1.RunEvent{Body: &streamv1.RunEvent_Waiting{
		Waiting: &streamv1.WaitingEvent{Missing: missing, Url: url},
	}})
}

func (s *Session) Resume() {
	s.waiting = false
	s.buildAttempt++
	s.emit(&streamv1.RunEvent{Body: &streamv1.RunEvent_Resumed{
		Resumed: &streamv1.ResumedEvent{Reason: "the page was answered"},
	}})
	if s.human != nil {
		s.human.Restart(buildStageID)
	}
	s.Building()
}

func (s *Session) Event(ev *progressv1.OperationEvent) {
	s.record(lift(ev))
}

func (s *Session) record(ev *streamv1.RunEvent) {
	if apps := s.emit(ev).GetOutcome().GetApps(); len(apps) > 0 {
		s.apps = apps
	}
}

func lift(ev *progressv1.OperationEvent) *streamv1.RunEvent {
	run := &streamv1.RunEvent{
		Level:   ev.GetLevel(),
		Phase:   ev.GetPhase(),
		Subject: ev.GetSubject(),
		Message: ev.GetMessage(),
		SpanId:  ev.GetSpanId(),
	}
	switch body := ev.GetBody().(type) {
	case *progressv1.OperationEvent_Started:
		run.Body = &streamv1.RunEvent_Started{Started: body.Started}
	case *progressv1.OperationEvent_Ended:
		run.Body = &streamv1.RunEvent_Ended{Ended: body.Ended}
	case *progressv1.OperationEvent_Output:
		run.Body = &streamv1.RunEvent_Output{Output: body.Output}
	case *progressv1.OperationEvent_Counter:
		run.Body = &streamv1.RunEvent_Counter{Counter: body.Counter}
	case *progressv1.OperationEvent_Plan:
		run.Body = &streamv1.RunEvent_Plan{Plan: body.Plan}
	case *progressv1.OperationEvent_DnsManualRecords:
		run.Body = &streamv1.RunEvent_DnsManualRecords{DnsManualRecords: body.DnsManualRecords}
	case *progressv1.OperationEvent_Result:
		run.Body = &streamv1.RunEvent_Outcome{Outcome: body.Result}
	}
	if ns := ev.GetTimeUnixNano(); ns > 0 {
		run.Time = timestamppb.New(time.Unix(0, ns))
	}
	return run
}

func (s *Session) Deployed(headline string, urlNotes []string, flip Flip) {
	s.result(&streamv1.RunResultEvent{
		Success:   true,
		Headline:  headline,
		UrlNotes:  urlNotes,
		FlipBound: flip.Bound,
	})
}

func (s *Session) Finish(headline string) {
	s.result(&streamv1.RunResultEvent{Success: true, Headline: headline})
}

func (s *Session) Fail(err error) {
	ev := &streamv1.RunResultEvent{Success: false, Detail: err.Error()}
	var refusal *envgate.Refusal
	if errors.As(err, &refusal) {
		ev.Missing = refusal.Missing()
		ev.Detail = strings.TrimLeft(strings.TrimPrefix(err.Error(), refusal.Error()), "\n")
	}
	s.result(ev)
}

func (s *Session) Cancel() {
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
	s.flushLines()
	ev.DurationMs = time.Since(s.start).Milliseconds()
	ev.LogPath = s.run.LogPath()
	ev.Apps = s.apps
	s.emit(&streamv1.RunEvent{Level: resultLevel(ev), Body: &streamv1.RunEvent_Result{Result: ev}})
}

func resultLevel(ev *streamv1.RunResultEvent) progressv1.Level {
	switch {
	case ev.GetInterrupted():
		return progressv1.Level_LEVEL_WARN
	case !ev.GetSuccess():
		return progressv1.Level_LEVEL_ERROR
	}
	return progressv1.Level_LEVEL_INFO
}

func (s *Session) Close() error {
	var err error
	s.closeOnce.Do(func() { err = s.shutdown() })
	return err
}

func (s *Session) interrupt() {
	s.closeOnce.Do(func() {
		s.Cancel()
		_ = s.shutdown()
	})
}

func (s *Session) shutdown() error {
	liveSessions.Delete(s)
	s.flushLines()
	return s.bus.Close()
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
