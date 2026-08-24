package deploy

import (
	"context"
	"crypto/rand"
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

func NewStage(parent Stage, title string) Stage {
	return Stage{ID: newStageID(), ParentID: parent.ID, Title: sanitizeTitle(title)}
}

type Attr struct {
	Key   progressv1.AttributeKey
	Value string
}

func AttrApp(name string) Attr {
	return Attr{progressv1.AttributeKey_ATTRIBUTE_KEY_APP, name}
}

func AttrResourceCount(n int) Attr {
	return Attr{progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_COUNT, strconv.Itoa(n)}
}

func AttrBytes(n int64) Attr {
	return Attr{progressv1.AttributeKey_ATTRIBUTE_KEY_BYTES, strconv.FormatInt(n, 10)}
}

func AttrDurationMS(d time.Duration) Attr {
	return Attr{progressv1.AttributeKey_ATTRIBUTE_KEY_DURATION_MS, strconv.FormatInt(d.Milliseconds(), 10)}
}

func AttrResourceType(typ string) Attr {
	return Attr{progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE, typ}
}

func AttrResourceName(name string) Attr {
	return Attr{progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME, name}
}

type Tracer interface {
	DeclareStages(final bool, stages ...Stage)
	Span(id, parentID StageID, name string, start, end time.Time, err error, attrs ...Attr)
}

func declareStages(t Tracer, final bool, stages ...Stage) {
	if t == nil {
		return
	}
	t.DeclareStages(final, stages...)
}

func spanForStage(t Tracer, stage Stage, start, end time.Time, err error, attrs ...Attr) {
	if t == nil {
		return
	}
	t.Span(stage.ID, stage.ParentID, stage.Title, start, end, err, attrs...)
}

func spanUnder(t Tracer, parent StageID, name string, start, end time.Time, err error, attrs ...Attr) {
	if t == nil {
		return
	}
	t.Span(newStageID(), parent, name, start, end, err, attrs...)
}

func DeclareStages(t Tracer, final bool, stages ...Stage) {
	declareStages(t, final, stages...)
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
