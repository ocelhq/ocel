package providerkit

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"strconv"
	"strings"
	"time"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type StageID [8]byte

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

type Stage struct {
	ID       StageID
	ParentID StageID
	Title    string
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

func NewRootStage(title string) Stage {
	return Stage{ID: newStageID(), Title: sanitizeTitle(title)}
}

func NewUnitStage(name string) Stage {
	title := sanitizeTitle(name)
	return Stage{ID: UnitStageID(title), Title: title}
}

func UnitStageID(name string) StageID {
	sum := sha256.Sum256([]byte("ocel:unit:" + name))
	var id StageID
	copy(id[:], sum[:])
	return id
}

func NewStage(parent Stage, title string) Stage {
	return Stage{ID: newStageID(), ParentID: parent.ID, Title: sanitizeTitle(title)}
}

type Attr struct {
	Key   string
	Value string
}

const (
	AttrKeyApp           = "app"
	AttrKeyResourceCount = "resource.count"
	AttrKeyBytes         = "bytes"
	AttrKeyDurationMS    = "duration.ms"
	AttrKeyResourceType  = "resource.type"
	AttrKeyResourceName  = "resource.name"
	AttrKeyErrorKind     = "error.kind"
)

var attributeKeys = map[string]progressv1.AttributeKey{
	AttrKeyApp:           progressv1.AttributeKey_ATTRIBUTE_KEY_APP,
	AttrKeyResourceCount: progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_COUNT,
	AttrKeyBytes:         progressv1.AttributeKey_ATTRIBUTE_KEY_BYTES,
	AttrKeyDurationMS:    progressv1.AttributeKey_ATTRIBUTE_KEY_DURATION_MS,
	AttrKeyResourceType:  progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE,
	AttrKeyResourceName:  progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME,
	AttrKeyErrorKind:     progressv1.AttributeKey_ATTRIBUTE_KEY_ERROR_KIND,
}

func AttributeKey(key string) progressv1.AttributeKey { return attributeKeys[key] }

func AttrApp(name string) Attr {
	return Attr{AttrKeyApp, name}
}

func AttrResourceCount(n int) Attr {
	return Attr{AttrKeyResourceCount, strconv.Itoa(n)}
}

func AttrBytes(n int64) Attr {
	return Attr{AttrKeyBytes, strconv.FormatInt(n, 10)}
}

func AttrDurationMS(d time.Duration) Attr {
	return Attr{AttrKeyDurationMS, strconv.FormatInt(d.Milliseconds(), 10)}
}

func AttrResourceType(typ string) Attr {
	return Attr{AttrKeyResourceType, typ}
}

func AttrResourceName(name string) Attr {
	return Attr{AttrKeyResourceName, name}
}

type Tracer interface {
	DeclareStages(final bool, stages ...Stage)
	Span(id, parentID StageID, name string, start, end time.Time, err error, attrs ...Attr)
}

func DeclareStages(t Tracer, final bool, stages ...Stage) {
	if t == nil {
		return
	}
	t.DeclareStages(final, stages...)
}

const (
	ErrorKindCanceled = "canceled"
	ErrorKindTimeout  = "timeout"
	ErrorKindFailed   = "failed"
)

func ClassifyError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return ErrorKindCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return ErrorKindTimeout
	default:
		return ErrorKindFailed
	}
}

type eventTracer struct {
	sender *eventSender
}

func newEventTracer(sender *eventSender) *eventTracer {
	return &eventTracer{sender: sender}
}

func (t *eventTracer) DeclareStages(final bool, stages ...Stage) {
	if len(stages) == 0 && !final {
		return
	}
	pb := make([]*progressv1.Stage, len(stages))
	for i, s := range stages {
		pb[i] = &progressv1.Stage{
			Id:       s.ID[:],
			ParentId: nonZeroStageID(s.ParentID),
			Title:    s.Title,
		}
	}
	t.sender.send(&progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_StagePlan{StagePlan: &progressv1.StagePlanEvent{
			Stages: pb,
			Final:  final,
		}},
	})
}

func (t *eventTracer) Span(id, parentID StageID, name string, start, end time.Time, err error, attrs ...Attr) {
	status := progressv1.SpanStatus_SPAN_STATUS_OK
	if err != nil {
		status = progressv1.SpanStatus_SPAN_STATUS_ERROR
		attrs = append(attrs, Attr{AttrKeyErrorKind, ClassifyError(err)})
	}

	pbAttrs := make([]*progressv1.SpanAttribute, len(attrs))
	for i, a := range attrs {
		pbAttrs[i] = &progressv1.SpanAttribute{Key: attributeKeys[a.Key], Value: a.Value}
	}

	t.sender.send(&progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Span{Span: &progressv1.SpanEvent{
			SpanId:            id[:],
			ParentSpanId:      nonZeroStageID(parentID),
			Name:              name,
			StartTimeUnixNano: start.UnixNano(),
			EndTimeUnixNano:   end.UnixNano(),
			Status:            status,
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
