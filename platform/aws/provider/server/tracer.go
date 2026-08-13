package server

import (
	"time"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
)

// eventTracer implements deploy.Tracer over an eventSender: every declared
// stage and every span it is asked to record goes out as a DeployEvent on
// the same stream as progress and log lines, through the one sender the
// AST guard in eventsender_guard_test.go requires.
type eventTracer struct {
	sender *eventSender
}

func newEventTracer(sender *eventSender) *eventTracer {
	return &eventTracer{sender: sender}
}

func (t *eventTracer) DeclareStages(final bool, stages ...deploy.Stage) {
	if len(stages) == 0 && !final {
		return
	}
	pb := make([]*deploymentsv1.Stage, len(stages))
	for i, s := range stages {
		pb[i] = &deploymentsv1.Stage{
			Id:       s.ID[:],
			ParentId: nonZeroStageID(s.ParentID),
			Title:    s.Title,
		}
	}
	t.sender.send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_StagePlan{StagePlan: &deploymentsv1.StagePlanEvent{
			Stages: pb,
			Final:  final,
		}},
	})
}

func (t *eventTracer) Span(id, parentID deploy.StageID, name string, start, end time.Time, err error, attrs ...deploy.Attr) {
	status := deploymentsv1.SpanStatus_SPAN_STATUS_OK
	if err != nil {
		status = deploymentsv1.SpanStatus_SPAN_STATUS_ERROR
		attrs = append(attrs, deploy.Attr{
			Key:   deploymentsv1.AttributeKey_ATTRIBUTE_KEY_ERROR_KIND,
			Value: deploy.ClassifyError(err),
		})
	}

	pbAttrs := make([]*deploymentsv1.SpanAttribute, len(attrs))
	for i, a := range attrs {
		pbAttrs[i] = &deploymentsv1.SpanAttribute{Key: a.Key, Value: a.Value}
	}

	t.sender.send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Span{Span: &deploymentsv1.SpanEvent{
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

// nonZeroStageID reports a StageID as proto bytes, or nil for the zero
// value: a top-level Stage or span has no parent, and Stage.parent_id /
// SpanEvent.parent_span_id are unset rather than eight zero bytes.
func nonZeroStageID(id deploy.StageID) []byte {
	if id == (deploy.StageID{}) {
		return nil
	}
	return id[:]
}
