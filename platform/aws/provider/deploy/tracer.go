package deploy

import (
	"context"
	"crypto/rand"
	"errors"
	"strconv"
	"time"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// StageID identifies a declared Stage, and doubles as the id of the span
// synthesised for that stage's execution: deployments.proto's Stage.id IS
// the span id.
type StageID [8]byte

func newStageID() StageID {
	var id StageID
	_, _ = rand.Read(id[:])
	return id
}

// Stage is one node of the deploy's stage plan.
type Stage struct {
	ID       StageID
	ParentID StageID
	Title    string
}

// NewRootStage mints a top-level Stage.
func NewRootStage(title string) Stage {
	return Stage{ID: newStageID(), Title: title}
}

// NewStage mints a Stage as a child of parent.
func NewStage(parent Stage, title string) Stage {
	return Stage{ID: newStageID(), ParentID: parent.ID, Title: title}
}

// Attr is one span attribute, bounded to the wire format's closed
// AttributeKey enum so a caller cannot invent a free-form key.
type Attr struct {
	Key   deploymentsv1.AttributeKey
	Value string
}

func AttrApp(name string) Attr {
	return Attr{deploymentsv1.AttributeKey_ATTRIBUTE_KEY_APP, name}
}

func AttrResourceCount(n int) Attr {
	return Attr{deploymentsv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_COUNT, strconv.Itoa(n)}
}

func AttrDurationMS(d time.Duration) Attr {
	return Attr{deploymentsv1.AttributeKey_ATTRIBUTE_KEY_DURATION_MS, strconv.FormatInt(d.Milliseconds(), 10)}
}

// Tracer receives the declared stage plan and the spans synthesised for it.
// A nil Tracer is a valid no-op so callers that don't need tracing (tests,
// paths other than Deploy) can leave it unset.
type Tracer interface {
	// DeclareStages sends a batch of newly-known stages. final marks the
	// point the stage tree stops growing.
	DeclareStages(final bool, stages ...Stage)

	// Span records one stage's execution, or a span for work that was
	// never itself a declared stage (a Pulumi resource operation),
	// parented under stage.ID. err drives SPAN_STATUS_ERROR and a bounded
	// ATTRIBUTE_KEY_ERROR_KIND classification — never the error's own
	// text.
	Span(id, parentID StageID, name string, start, end time.Time, err error, attrs ...Attr)
}

func declareStages(t Tracer, final bool, stages ...Stage) {
	if t == nil {
		return
	}
	t.DeclareStages(final, stages...)
}

// spanForStage records the span for a declared Stage's own execution: its
// span id is the stage's id, and its name is the stage's title, so the
// stage plan the user sees and the trace's spans are the same structure.
func spanForStage(t Tracer, stage Stage, start, end time.Time, err error, attrs ...Attr) {
	if t == nil {
		return
	}
	t.Span(stage.ID, stage.ParentID, stage.Title, start, end, err, attrs...)
}

// spanUnder records a span for work that is not itself a declared stage
// (a Pulumi resource batch, or an individual resource operation), parented
// under an existing stage or span id, with a freshly minted id of its own.
func spanUnder(t Tracer, parent StageID, name string, start, end time.Time, err error, attrs ...Attr) {
	if t == nil {
		return
	}
	t.Span(newStageID(), parent, name, start, end, err, attrs...)
}

// Bounded ATTRIBUTE_KEY_ERROR_KIND classifications. Never derived from an
// error's own text: Pulumi errors routinely embed resource properties,
// provider responses, connection strings and presigned URLs.
const (
	ErrorKindCanceled = "canceled"
	ErrorKindTimeout  = "timeout"
	ErrorKindFailed   = "failed"
)

// ClassifyError buckets err into a bounded, secret-free classification.
// It never calls err.Error() or otherwise inspects the error's text.
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
