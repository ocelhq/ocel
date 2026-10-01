package agent

import (
	"context"
	"errors"
	"net/http"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/environment"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/task/v1/taskv1connect"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/topic/v1/topicv1connect"
)

type Queues interface {
	Served(tier environment.Tier, project, env string) (taskv1connect.TaskServiceHandler, topicv1connect.TopicServiceHandler, bool)
}

var (
	_ taskv1connect.TaskServiceHandler   = callerTasks{}
	_ topicv1connect.TopicServiceHandler = callerTopics{}
)

func (s *Server) mountQueues(mux *http.ServeMux) {
	mux.Handle(taskv1connect.NewTaskServiceHandler(callerTasks{s}))
	mux.Handle(topicv1connect.NewTopicServiceHandler(callerTopics{s}))
}

func (s *Server) callerQueue(ctx context.Context) (taskv1connect.TaskServiceHandler, topicv1connect.TopicServiceHandler, error) {
	manifest, refused := s.callerManifest(ctx)
	if refused != nil {
		code := connect.CodeUnavailable
		switch refused.status {
		case http.StatusForbidden:
			code = connect.CodePermissionDenied
		case http.StatusNotFound:
			code = connect.CodeFailedPrecondition
		}
		return nil, nil, connect.NewError(code, refused)
	}
	if manifest.Queue == "" {
		return nil, nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this deployment declares no task or topic, so there is nothing to trigger or send to"))
	}
	tasks, topics, served := s.Queues.Served(environment.Tier(manifest.Tier), manifest.Slug, manifest.Queue)
	if !served {
		return nil, nil, connect.NewError(connect.CodeUnavailable,
			errors.New("this box is not serving "+manifest.Slug+"'s "+manifest.Queue+" queue yet: its queue database is starting, or the box agent predates topics and tasks and `ocel bootstrap` has not been run since"))
	}
	return tasks, topics, nil
}

func callTasks[Req, Res any](ctx context.Context, s *Server, req *Req, method func(taskv1connect.TaskServiceHandler, context.Context, *Req) (*Res, error)) (*Res, error) {
	tasks, _, err := s.callerQueue(ctx)
	if err != nil {
		return nil, err
	}
	return method(tasks, ctx, req)
}

func callTopics[Req, Res any](ctx context.Context, s *Server, req *Req, method func(topicv1connect.TopicServiceHandler, context.Context, *Req) (*Res, error)) (*Res, error) {
	_, topics, err := s.callerQueue(ctx)
	if err != nil {
		return nil, err
	}
	return method(topics, ctx, req)
}

type callerTasks struct{ s *Server }

func (c callerTasks) Trigger(ctx context.Context, req *taskv1.TriggerRequest) (*taskv1.TriggerResponse, error) {
	return callTasks(ctx, c.s, req, taskv1connect.TaskServiceHandler.Trigger)
}

func (c callerTasks) BatchTrigger(ctx context.Context, req *taskv1.BatchTriggerRequest) (*taskv1.BatchTriggerResponse, error) {
	return callTasks(ctx, c.s, req, taskv1connect.TaskServiceHandler.BatchTrigger)
}

func (c callerTasks) RetrieveRun(ctx context.Context, req *taskv1.RetrieveRunRequest) (*taskv1.RetrieveRunResponse, error) {
	return callTasks(ctx, c.s, req, taskv1connect.TaskServiceHandler.RetrieveRun)
}

func (c callerTasks) ListRuns(ctx context.Context, req *taskv1.ListRunsRequest) (*taskv1.ListRunsResponse, error) {
	return callTasks(ctx, c.s, req, taskv1connect.TaskServiceHandler.ListRuns)
}

func (c callerTasks) CancelRun(ctx context.Context, req *taskv1.CancelRunRequest) (*taskv1.CancelRunResponse, error) {
	return callTasks(ctx, c.s, req, taskv1connect.TaskServiceHandler.CancelRun)
}

func (c callerTasks) ReplayRun(ctx context.Context, req *taskv1.ReplayRunRequest) (*taskv1.ReplayRunResponse, error) {
	return callTasks(ctx, c.s, req, taskv1connect.TaskServiceHandler.ReplayRun)
}

func (c callerTasks) RescheduleRun(ctx context.Context, req *taskv1.RescheduleRunRequest) (*taskv1.RescheduleRunResponse, error) {
	return callTasks(ctx, c.s, req, taskv1connect.TaskServiceHandler.RescheduleRun)
}

type callerTopics struct{ s *Server }

func (c callerTopics) Send(ctx context.Context, req *topicv1.SendRequest) (*topicv1.SendResponse, error) {
	return callTopics(ctx, c.s, req, topicv1connect.TopicServiceHandler.Send)
}

func (c callerTopics) ListDeadLetters(ctx context.Context, req *topicv1.ListDeadLettersRequest) (*topicv1.ListDeadLettersResponse, error) {
	return callTopics(ctx, c.s, req, topicv1connect.TopicServiceHandler.ListDeadLetters)
}

func (c callerTopics) RedriveDeadLetters(ctx context.Context, req *topicv1.RedriveDeadLettersRequest) (*topicv1.RedriveDeadLettersResponse, error) {
	return callTopics(ctx, c.s, req, topicv1connect.TopicServiceHandler.RedriveDeadLetters)
}

func (c callerTopics) PurgeDeadLetters(ctx context.Context, req *topicv1.PurgeDeadLettersRequest) (*topicv1.PurgeDeadLettersResponse, error) {
	return callTopics(ctx, c.s, req, topicv1connect.TopicServiceHandler.PurgeDeadLetters)
}

func (c callerTopics) CountDeadLetters(ctx context.Context, req *topicv1.CountDeadLettersRequest) (*topicv1.CountDeadLettersResponse, error) {
	return callTopics(ctx, c.s, req, topicv1connect.TopicServiceHandler.CountDeadLetters)
}
