package providerserver

import (
	"crypto/rand"
	"errors"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/progressproto"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type SpanID [naming.SpanIDLen]byte

func newSpanID() SpanID {
	var id SpanID
	if _, err := rand.Read(id[:]); err != nil {
		panic("mint span id: " + err.Error())
	}
	if id == (SpanID{}) {
		return newSpanID()
	}
	return id
}

func derivedSpanID(raw []byte) SpanID {
	var id SpanID
	copy(id[:], raw)
	return id
}

type Span struct {
	ID       SpanID
	ParentID SpanID
	Title    progress.Title
	Phase    progressv1.Phase
	Subject  string
}

func (s Span) event(ev *progressv1.OperationEvent) *progressv1.OperationEvent {
	ev.Phase = s.Phase
	ev.Subject = s.Subject
	ev.SpanId = s.ID[:]
	return ev
}

func sanitizeTitle(title progress.Title) progress.Title {
	return progress.Title{Started: progress.SanitizeSpanName(title.Started), Ended: progress.SanitizeSpanName(title.Ended)}
}

func UnitSpan(name, subject string, title progress.Title, phase progressv1.Phase) Span {
	return Span{
		ID:      derivedSpanID(naming.UnitID(name)),
		Title:   sanitizeTitle(title),
		Phase:   phase,
		Subject: subject,
	}
}

func NewSpan(parent Span, name string) Span {
	name = progress.SanitizeSpanName(name)
	return Span{ID: newSpanID(), ParentID: parent.ID, Title: progress.Title{Started: name, Ended: name}, Phase: parent.Phase, Subject: parent.Subject}
}

type spanRun struct {
	events  *spanEvents
	span    Span
	said    error
	partial string
}

func (u *spanRun) recordPartial(result string) { u.partial = result }

func (u *spanRun) phase(do func(progress.Log) error) error {
	progress := newSpanLog(u.events.sender, u.span)
	err := do(progress)
	if err != nil {
		progress.Error(err.Error())
		u.said = err
	}
	return err
}

type spanEvents struct {
	sender *eventStream
}

func newSpanEvents(sender *eventStream) *spanEvents {
	return &spanEvents{sender: sender}
}

func (t *spanEvents) run(span Span, do func(*spanRun) error) error {
	start := time.Now()
	t.Start(start, span)
	run := &spanRun{events: t, span: span}
	err := do(run)
	if err != nil && !errors.Is(err, run.said) {
		newSpanLog(t.sender, span).Error(err.Error())
	}
	if err == nil && run.partial != "" {
		t.EndPartial(span, start, time.Now(), run.partial)
		return nil
	}
	t.End(span, start, time.Now(), err)
	return err
}

func (t *spanEvents) Start(at time.Time, spans ...Span) {
	for _, s := range spans {
		t.sender.send(s.event(&progressv1.OperationEvent{
			Time:    timestamppb.New(at),
			Message: s.Title.Started,
			Body: &progressv1.OperationEvent_Started{Started: &progressv1.Started{
				ParentSpanId: nonZeroSpanID(s.ParentID),
			}},
		}))
	}
}

func (t *spanEvents) End(span Span, start, end time.Time, err error, attrs ...progress.Attr) {
	if err != nil {
		attrs = append(attrs, progress.Attr{Key: progress.AttrKeyErrorKind, Value: provider.ClassifyError(err)})
		t.ended(span, start, end, progressv1.SpanStatus_SPAN_STATUS_ERROR, progressv1.Level_LEVEL_ERROR, "", attrs)
		return
	}
	t.ended(span, start, end, progressv1.SpanStatus_SPAN_STATUS_OK, progressv1.Level_LEVEL_INFO, span.Title.Ended, attrs)
}

func (t *spanEvents) EndPartial(span Span, start, end time.Time, result string) {
	t.ended(span, start, end, progressv1.SpanStatus_SPAN_STATUS_OK, progressv1.Level_LEVEL_WARN, progress.SanitizeSpanName(result), nil)
}

func (t *spanEvents) ended(span Span, start, end time.Time, status progressv1.SpanStatus, level progressv1.Level, title string, attrs []progress.Attr) {
	t.sender.send(&progressv1.OperationEvent{
		Time:    timestamppb.New(end),
		Level:   level,
		Phase:   span.Phase,
		Subject: span.Subject,
		SpanId:  span.ID[:],
		Body: &progressv1.OperationEvent_Ended{Ended: &progressv1.Ended{
			Status:            status,
			StartTimeUnixNano: start.UnixNano(),
			Attributes:        progressproto.EncodeAttrs(attrs),
			Title:             title,
		}},
	})
}

func nonZeroSpanID(id SpanID) []byte {
	if id == (SpanID{}) {
		return nil
	}
	return id[:]
}
