package providerserver

import (
	"crypto/rand"
	"errors"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type StageID [naming.StageIDLen]byte

func newStageID() StageID {
	var id StageID
	if _, err := rand.Read(id[:]); err != nil {
		panic("mint stage id: " + err.Error())
	}
	if id == (StageID{}) {
		return newStageID()
	}
	return id
}

func derivedStageID(raw []byte) StageID {
	var id StageID
	copy(id[:], raw)
	return id
}

type Stage struct {
	ID       StageID
	ParentID StageID
	Title    string
	Phase    progressv1.Phase
	Subject  string
}

func (s Stage) scoped(ev *progressv1.OperationEvent) *progressv1.OperationEvent {
	ev.Phase = s.Phase
	ev.Subject = s.Subject
	ev.SpanId = s.ID[:]
	return ev
}

const maxStageTitleLen = 200

func stripControlChars(s string, capLen int) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
		if capLen > 0 && b.Len() >= capLen {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

func sanitizeTitle(title string) string {
	out := stripControlChars(title, maxStageTitleLen)
	if out == "" {
		return "stage"
	}
	return out
}

func sanitizeMessage(msg string) string {
	return stripControlChars(msg, 0)
}

func UnitStage(name, subject, title string, phase progressv1.Phase) Stage {
	return Stage{
		ID:      derivedStageID(naming.UnitID(name)),
		Title:   sanitizeTitle(title),
		Phase:   phase,
		Subject: subject,
	}
}

func NewStage(parent Stage, title string) Stage {
	return Stage{ID: newStageID(), ParentID: parent.ID, Title: sanitizeTitle(title), Phase: parent.Phase, Subject: parent.Subject}
}

var attributeKeys = map[string]progressv1.AttributeKey{
	provider.AttrKeyApp:            progressv1.AttributeKey_ATTRIBUTE_KEY_APP,
	provider.AttrKeyResourceCount:  progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_COUNT,
	provider.AttrKeyBytes:          progressv1.AttributeKey_ATTRIBUTE_KEY_BYTES,
	provider.AttrKeyDurationMS:     progressv1.AttributeKey_ATTRIBUTE_KEY_DURATION_MS,
	provider.AttrKeyResourceType:   progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE,
	provider.AttrKeyResourceName:   progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME,
	provider.AttrKeyErrorKind:      progressv1.AttributeKey_ATTRIBUTE_KEY_ERROR_KIND,
	provider.AttrKeyResourceAction: progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_ACTION,
}

func AttributeKey(key string) progressv1.AttributeKey { return attributeKeys[key] }

type stageScope struct {
	sender *eventStream
	trace  *eventTrace
}

func newStageScope(sender *eventStream) *stageScope {
	return &stageScope{sender: sender, trace: newEventTrace(sender)}
}

func (s *stageScope) unit(stage Stage, do func(*unitRun) error) error {
	start := time.Now()
	s.trace.Start(start, stage)
	run := &unitRun{scope: s, stage: stage}
	err := do(run)
	if err != nil && !errors.Is(err, run.said) {
		newProgress(s.sender, stage).Error(err.Error())
	}
	if err == nil && run.partial != "" {
		s.trace.EndPartial(stage, start, time.Now(), run.partial)
		return nil
	}
	s.trace.End(stage, start, time.Now(), err)
	return err
}

type unitRun struct {
	scope   *stageScope
	stage   Stage
	said    error
	partial string
}

func (u *unitRun) recordPartial(result string) { u.partial = result }

func (u *unitRun) phase(do func(edge.Progress) error) error {
	progress := newProgress(u.scope.sender, u.stage)
	err := do(progress)
	if err != nil {
		progress.Error(err.Error())
		u.said = err
	}
	return err
}

type eventTrace struct {
	sender *eventStream
}

func newEventTrace(sender *eventStream) *eventTrace {
	return &eventTrace{sender: sender}
}

func (t *eventTrace) Start(at time.Time, stages ...Stage) {
	for _, s := range stages {
		t.sender.send(s.scoped(&progressv1.OperationEvent{
			TimeUnixNano: at.UnixNano(),
			Message:      s.Title,
			Body: &progressv1.OperationEvent_Started{Started: &progressv1.Started{
				ParentSpanId: nonZeroStageID(s.ParentID),
			}},
		}))
	}
}

func (t *eventTrace) End(stage Stage, start, end time.Time, err error, attrs ...edge.Attr) {
	status, level := progressv1.SpanStatus_SPAN_STATUS_OK, progressv1.Level_LEVEL_INFO
	if err != nil {
		status, level = progressv1.SpanStatus_SPAN_STATUS_ERROR, progressv1.Level_LEVEL_ERROR
		attrs = append(attrs, edge.Attr{Key: provider.AttrKeyErrorKind, Value: provider.ClassifyError(err)})
	}
	t.ended(stage, start, end, status, level, "", attrs)
}

func (t *eventTrace) EndPartial(stage Stage, start, end time.Time, result string) {
	t.ended(stage, start, end, progressv1.SpanStatus_SPAN_STATUS_OK, progressv1.Level_LEVEL_WARN, result, nil)
}

func (t *eventTrace) ended(stage Stage, start, end time.Time, status progressv1.SpanStatus, level progressv1.Level, message string, attrs []edge.Attr) {
	pbAttrs := make([]*progressv1.SpanAttribute, len(attrs))
	for i, a := range attrs {
		pbAttrs[i] = &progressv1.SpanAttribute{Key: attributeKeys[a.Key], Value: a.Value}
	}

	t.sender.send(&progressv1.OperationEvent{
		TimeUnixNano: end.UnixNano(),
		Level:        level,
		Phase:        stage.Phase,
		Subject:      stage.Subject,
		Message:      message,
		SpanId:       stage.ID[:],
		Body: &progressv1.OperationEvent_Ended{Ended: &progressv1.Ended{
			Status:            status,
			StartTimeUnixNano: start.UnixNano(),
			Attributes:        pbAttrs,
		}},
	})
}

func nonZeroStageID(id StageID) []byte {
	if id == (StageID{}) {
		return nil
	}
	return id[:]
}
