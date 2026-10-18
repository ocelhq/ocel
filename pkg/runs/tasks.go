package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"connectrpc.com/connect"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/task/v1/taskv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
)

var _ taskv1connect.TaskServiceHandler = Tasks{}

type Tasks struct {
	engine *Engine
}

func (e *Engine) Tasks() Tasks { return Tasks{engine: e} }

func (t Tasks) task(name string) (deployedConsumer, error) {
	deployed, found := t.engine.task(name)
	if !found {
		return deployedConsumer{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("no task named %q is deployed", name))
	}
	return deployed, nil
}

func (t Tasks) Trigger(ctx context.Context, req *taskv1.TriggerRequest) (*taskv1.TriggerResponse, error) {
	deployed, err := t.task(req.GetTask())
	if err != nil {
		return nil, err
	}
	id, err := t.engine.trigger(ctx, deployed, req.GetPayload(), req.GetOptions())
	if err != nil {
		return nil, err
	}
	return &taskv1.TriggerResponse{Id: id}, nil
}

func (t Tasks) BatchTrigger(ctx context.Context, req *taskv1.BatchTriggerRequest) (*taskv1.BatchTriggerResponse, error) {
	deployed, err := t.task(req.GetTask())
	if err != nil {
		return nil, err
	}
	for i, item := range req.GetItems() {
		if err := refuseInvalidTrigger(item.GetPayload(), item.GetOptions()); err != nil {
			return nil, connect.NewError(connect.CodeOf(err), fmt.Errorf("item %d: %w", i, err))
		}
	}
	ids := make([]string, 0, len(req.GetItems()))
	for i, item := range req.GetItems() {
		id, err := t.engine.trigger(ctx, deployed, item.GetPayload(), item.GetOptions())
		if err != nil {
			return &taskv1.BatchTriggerResponse{Ids: ids}, connect.NewError(connect.CodeOf(err), fmt.Errorf("item %d failed after runs %v were triggered: %w", i, ids, err))
		}
		ids = append(ids, id)
	}
	return &taskv1.BatchTriggerResponse{Ids: ids}, nil
}

func (t Tasks) RetrieveRun(ctx context.Context, req *taskv1.RetrieveRunRequest) (*taskv1.RetrieveRunResponse, error) {
	run, err := t.engine.readTaskRun(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	return &taskv1.RetrieveRunResponse{Run: t.engine.runMessageAt(run, time.Now())}, nil
}

func (e *Engine) runMessageAt(run Run, now time.Time) *taskv1.Run {
	_, deployed := e.consumer(run.Topic, run.Consumer)
	run.Status = statusAt(run, now, deployed)
	return runMessageOf(run)
}

func (t Tasks) ListRuns(ctx context.Context, req *taskv1.ListRunsRequest) (*taskv1.ListRunsResponse, error) {
	var topics []string
	if req.GetTask() != "" {
		if _, err := t.task(req.GetTask()); err == nil {
			topics = []string{req.GetTask()}
		}
	} else {
		for _, name := range slices.Sorted(maps.Keys(t.engine.current().Topics)) {
			if _, isTask := t.engine.task(name); isTask {
				topics = append(topics, name)
			}
		}
	}
	if len(topics) == 0 {
		return &taskv1.ListRunsResponse{}, nil
	}
	asked := StatusesOf(req.GetStatuses())
	page, err := t.engine.cfg.Store.ListRuns(ctx, RunFilter{
		Topics:   topics,
		Statuses: storedStatusesFor(asked),
		Tags:     req.GetTags(),
		Cursor:   req.GetCursor(),
		Limit:    int(req.GetLimit()),
	})
	if errors.Is(err, ErrUnknownCursor) {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err != nil {
		return nil, err
	}
	now := time.Now()
	resp := &taskv1.ListRunsResponse{NextCursor: page.NextCursor}
	for _, run := range page.Runs {
		listed := t.engine.runMessageAt(run, now)
		if len(asked) == 0 || slices.Contains(asked, statusNames[listed.GetStatus()]) {
			resp.Runs = append(resp.Runs, listed)
		}
	}
	return resp, nil
}

func storedStatusesFor(asked []provider.RunStatus) []provider.RunStatus {
	if len(asked) == 0 {
		return nil
	}
	stored := slices.Clone(asked)
	for _, status := range asked {
		if status == provider.RunQueued || status == provider.RunExpired || status == provider.RunCanceled {
			for _, unfinished := range []provider.RunStatus{provider.RunDelayed, provider.RunQueued, provider.RunExecuting} {
				if !slices.Contains(stored, unfinished) {
					stored = append(stored, unfinished)
				}
			}
		}
	}
	return stored
}

func (t Tasks) CancelRun(ctx context.Context, req *taskv1.CancelRunRequest) (*taskv1.CancelRunResponse, error) {
	run, err := t.engine.readTaskRun(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	run, err = t.engine.cancel(ctx, run, "")
	if err != nil {
		return nil, err
	}
	return &taskv1.CancelRunResponse{Run: t.engine.runMessageAt(run, time.Now())}, nil
}

func (e *Engine) cancel(ctx context.Context, run Run, reason string) (Run, error) {
	execution := run.Execution
	for range maxRaces {
		if !IsUnfinished(run.Status) {
			return run, nil
		}
		canceled := run
		canceled.cancel(reason, time.Now())
		written, err := e.write(ctx, canceled)
		if errors.Is(err, records.ErrStale) {
			if run, err = e.reread(ctx, run); err != nil {
				return Run{}, refuseGone(execution, err)
			}
			continue
		}
		if err != nil {
			return Run{}, err
		}
		e.stopInFlight(execution)
		e.release(ctx, run)
		return written, nil
	}
	return Run{}, connect.NewError(connect.CodeAborted, fmt.Errorf("run %q changed under each of %d tries to cancel it", execution, maxRaces))
}

func (t Tasks) RescheduleRun(ctx context.Context, req *taskv1.RescheduleRunRequest) (*taskv1.RescheduleRunResponse, error) {
	task, ok := taskOf(req.GetId())
	if !ok {
		return nil, refuseMissingRun(req.GetId())
	}
	deployed, err := t.task(task)
	if err != nil {
		return nil, err
	}
	due := req.GetDueAt().AsTime()
	if err := t.engine.refuseDelay(deployed.topicName, deployed.topic, due, time.Now()); err != nil {
		return nil, err
	}
	run, err := t.engine.reschedule(ctx, deployed, req.GetId(), due, false)
	if err != nil {
		return nil, err
	}
	return &taskv1.RescheduleRunResponse{Run: t.engine.runMessageAt(run, time.Now())}, nil
}

func (t Tasks) ReplayRun(ctx context.Context, req *taskv1.ReplayRunRequest) (*taskv1.ReplayRunResponse, error) {
	original, err := t.engine.readTaskRun(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	deployed, err := t.task(original.Topic)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	replay := runRequest{
		payload:     original.Payload,
		dueAt:       now,
		key:         original.Key,
		lane:        original.Lane,
		maxAttempts: int32(original.MaxAttempts),
		ttl:         deployed.topic.TTL,
		tags:        original.Tags,
		metadata:    original.Metadata,
	}
	if !original.ExpiresAt.IsZero() && !original.DueAt.IsZero() {
		replay.ttl = original.ExpiresAt.Sub(original.DueAt)
	}
	run := newTaskRun(deployed, NewMessageID(now), now, replay)
	if err := t.engine.start(ctx, run); err != nil {
		return nil, err
	}
	return &taskv1.ReplayRunResponse{Id: run.Execution}, nil
}

func (e *Engine) FireCron(ctx context.Context, task string, scheduled time.Time) error {
	deployed, found := e.task(task)
	if !found {
		return fmt.Errorf("no task named %q is deployed to run on its schedule", task)
	}
	_, err := e.trigger(ctx, deployed, CronPayload(scheduled), &taskv1.TriggerOptions{IdempotencyKey: "cron-" + cronStamp(scheduled)})
	return err
}

func cronStamp(scheduled time.Time) string {
	return scheduled.UTC().Truncate(time.Minute).Format(time.RFC3339)
}

func CronPayload(scheduled time.Time) json.RawMessage {
	payload, _ := json.Marshal(map[string]string{"timestamp": cronStamp(scheduled)})
	return payload
}

func (e *Engine) CancelRunsOf(ctx context.Context, topic, consumer, reason string) error {
	filter := RunFilter{Topics: []string{topic}, Consumer: consumer, Statuses: []provider.RunStatus{provider.RunDelayed, provider.RunQueued, provider.RunExecuting}}
	for {
		page, err := e.cfg.Store.ListRuns(ctx, filter)
		if err != nil {
			return err
		}
		for _, run := range page.Runs {
			if _, err := e.cancel(ctx, run, reason); err != nil && connect.CodeOf(err) != connect.CodeNotFound {
				return err
			}
		}
		if page.NextCursor == "" {
			return nil
		}
		filter.Cursor = page.NextCursor
	}
}
