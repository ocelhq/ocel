package queue

import (
	"context"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/task/v1/taskv1connect"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/topic/v1/topicv1connect"
	"github.com/ocelhq/ocel/platform/pgmq"
)

var (
	_ taskv1connect.TaskServiceHandler   = tasks{}
	_ topicv1connect.TopicServiceHandler = topics{}
)

type tasks struct{ backend *Backend }

type topics struct{ backend *Backend }

func call[Service, Req, Res any](ctx context.Context, b *Backend, req *Req, service func(*pgmq.Engine) Service, method func(Service, context.Context, *Req) (*Res, error)) (*Res, error) {
	engine, err := b.current()
	if err != nil {
		return nil, err
	}
	return method(service(engine), ctx, req)
}

func (t tasks) Trigger(ctx context.Context, req *taskv1.TriggerRequest) (*taskv1.TriggerResponse, error) {
	return call(ctx, t.backend, req, (*pgmq.Engine).Tasks, pgmq.Tasks.Trigger)
}

func (t tasks) BatchTrigger(ctx context.Context, req *taskv1.BatchTriggerRequest) (*taskv1.BatchTriggerResponse, error) {
	return call(ctx, t.backend, req, (*pgmq.Engine).Tasks, pgmq.Tasks.BatchTrigger)
}

func (t tasks) RetrieveRun(ctx context.Context, req *taskv1.RetrieveRunRequest) (*taskv1.RetrieveRunResponse, error) {
	return call(ctx, t.backend, req, (*pgmq.Engine).Tasks, pgmq.Tasks.RetrieveRun)
}

func (t tasks) ListRuns(ctx context.Context, req *taskv1.ListRunsRequest) (*taskv1.ListRunsResponse, error) {
	return call(ctx, t.backend, req, (*pgmq.Engine).Tasks, pgmq.Tasks.ListRuns)
}

func (t tasks) CancelRun(ctx context.Context, req *taskv1.CancelRunRequest) (*taskv1.CancelRunResponse, error) {
	return call(ctx, t.backend, req, (*pgmq.Engine).Tasks, pgmq.Tasks.CancelRun)
}

func (t tasks) ReplayRun(ctx context.Context, req *taskv1.ReplayRunRequest) (*taskv1.ReplayRunResponse, error) {
	return call(ctx, t.backend, req, (*pgmq.Engine).Tasks, pgmq.Tasks.ReplayRun)
}

func (t tasks) RescheduleRun(ctx context.Context, req *taskv1.RescheduleRunRequest) (*taskv1.RescheduleRunResponse, error) {
	return call(ctx, t.backend, req, (*pgmq.Engine).Tasks, pgmq.Tasks.RescheduleRun)
}

func (t topics) Send(ctx context.Context, req *topicv1.SendRequest) (*topicv1.SendResponse, error) {
	return call(ctx, t.backend, req, (*pgmq.Engine).Topics, pgmq.Topics.Send)
}

func (t topics) ListDeadLetters(ctx context.Context, req *topicv1.ListDeadLettersRequest) (*topicv1.ListDeadLettersResponse, error) {
	return call(ctx, t.backend, req, (*pgmq.Engine).Topics, pgmq.Topics.ListDeadLetters)
}

func (t topics) RedriveDeadLetters(ctx context.Context, req *topicv1.RedriveDeadLettersRequest) (*topicv1.RedriveDeadLettersResponse, error) {
	return call(ctx, t.backend, req, (*pgmq.Engine).Topics, pgmq.Topics.RedriveDeadLetters)
}

func (t topics) PurgeDeadLetters(ctx context.Context, req *topicv1.PurgeDeadLettersRequest) (*topicv1.PurgeDeadLettersResponse, error) {
	return call(ctx, t.backend, req, (*pgmq.Engine).Topics, pgmq.Topics.PurgeDeadLetters)
}

func (t topics) CountDeadLetters(ctx context.Context, req *topicv1.CountDeadLettersRequest) (*topicv1.CountDeadLettersResponse, error) {
	return call(ctx, t.backend, req, (*pgmq.Engine).Topics, pgmq.Topics.CountDeadLetters)
}
