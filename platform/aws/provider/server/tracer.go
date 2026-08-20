package server

import (
	"time"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/progress/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
)

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

func (t *eventTracer) Span(id, parentID deploy.StageID, name string, start, end time.Time, err error, attrs ...deploy.Attr) {
	status := progressv1.SpanStatus_SPAN_STATUS_OK
	if err != nil {
		status = progressv1.SpanStatus_SPAN_STATUS_ERROR
		attrs = append(attrs, deploy.Attr{
			Key:   progressv1.AttributeKey_ATTRIBUTE_KEY_ERROR_KIND,
			Value: deploy.ClassifyError(err),
		})
	}

	pbAttrs := make([]*progressv1.SpanAttribute, len(attrs))
	for i, a := range attrs {
		pbAttrs[i] = &progressv1.SpanAttribute{Key: a.Key, Value: a.Value}
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

func nonZeroStageID(id deploy.StageID) []byte {
	if id == (deploy.StageID{}) {
		return nil
	}
	return id[:]
}
