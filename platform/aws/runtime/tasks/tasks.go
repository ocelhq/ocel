package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/envelope"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/task/v1/taskv1connect"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

var _ taskv1connect.TaskServiceHandler = Tasks{}

type Tasks struct {
	engine *Engine
}

func (e *Engine) Tasks() Tasks { return Tasks{engine: e} }

type recordedRun struct {
	Run string `json:"run"`
}

func runRecord(execution string) json.RawMessage {
	encoded, _ := json.Marshal(recordedRun{Run: execution})
	return encoded
}

func readRecordedRun(value json.RawMessage) string {
	var recorded recordedRun
	_ = json.Unmarshal(value, &recorded)
	return recorded.Run
}

func (t Tasks) task(name string) (deployedConsumer, error) {
	deployed, found := t.engine.taskConsumer(name)
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
	id, err := t.trigger(ctx, deployed, req.GetPayload(), req.GetOptions())
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
	ids := make([]string, 0, len(req.GetItems()))
	for _, item := range req.GetItems() {
		id, err := t.trigger(ctx, deployed, item.GetPayload(), item.GetOptions())
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return &taskv1.BatchTriggerResponse{Ids: ids}, nil
}

type runRequest struct {
	payload     json.RawMessage
	dueAt       time.Time
	key         string
	lane        topicv1.Lane
	maxAttempts int32
	ttl         time.Duration
	tags        []string
	metadata    json.RawMessage
}

func (t Tasks) trigger(ctx context.Context, deployed deployedConsumer, payload []byte, options *taskv1.TriggerOptions) (string, error) {
	if err := runs.RefuseNonJSON(payload); err != nil {
		return "", err
	}
	now := time.Now()
	run := runRequest{
		payload:     payload,
		dueAt:       now,
		key:         options.GetKey(),
		lane:        options.GetLane(),
		maxAttempts: options.GetMaxAttempts(),
		ttl:         deployed.topic.TTL,
		tags:        options.GetTags(),
	}
	if options.GetDueAt() != nil {
		run.dueAt = options.GetDueAt().AsTime()
	}
	if options.GetTtl() != nil {
		run.ttl = options.GetTtl().AsDuration()
	}
	if metadata := options.GetMetadata(); len(metadata) > 0 {
		if err := runs.RefuseNonObject(metadata); err != nil {
			return "", err
		}
		run.metadata = metadata
	}
	messageID := envelope.NewMessageID(now)
	execution := runs.ExecutionOf(messageID, deployed.consumer.Name)
	store := t.engine.store
	idempotencyKey := options.GetIdempotencyKey()
	life := provider.DefaultIdempotencyKeyLife
	if options.GetIdempotencyKeyTtl() != nil {
		life = options.GetIdempotencyKeyTtl().AsDuration()
	}
	if idempotencyKey != "" {
		held, created, err := store.EnsureRecord(ctx, provider.ExpiringRecord{Purpose: provider.RecordIdempotency, Topic: deployed.topicName, Key: idempotencyKey, Value: runRecord(execution), ExpiresAt: now.Add(life)})
		if err != nil {
			return "", err
		}
		if !created {
			return readRecordedRun(held.Value), nil
		}
	}
	if debounce := options.GetDebounce(); debounce != nil {
		run.dueAt = now.Add(debounce.GetDelay().AsDuration())
		pending, err := t.debounce(ctx, deployed, debounce.GetKey(), execution, run.dueAt)
		if err != nil {
			return "", err
		}
		if pending != "" {
			if idempotencyKey != "" {
				if err := store.rewriteRecord(ctx, provider.ExpiringRecord{Purpose: provider.RecordIdempotency, Topic: deployed.topicName, Key: idempotencyKey, Value: runRecord(pending), ExpiresAt: now.Add(life)}); err != nil {
					return "", err
				}
			}
			return pending, nil
		}
	}
	if err := t.engine.startRun(ctx, deployed, messageID, now, execution, run); err != nil {
		if idempotencyKey != "" {
			_ = store.deleteRecord(ctx, provider.RecordIdempotency, deployed.topicName, idempotencyKey)
		}
		return "", err
	}
	return execution, nil
}

func (e *Engine) startRun(ctx context.Context, deployed deployedConsumer, messageID string, now time.Time, execution string, run runRequest) error {
	token := envelope.NewMessageID(now)
	status := provider.RunQueued
	if run.dueAt.After(now) {
		status = provider.RunDelayed
	}
	item := runItem{
		SK:                execution,
		Topic:             deployed.topicName,
		Consumer:          deployed.consumer.Name,
		Status:            string(status),
		Payload:           string(run.payload),
		MaxAttempts:       runs.AttemptsFor(deployed.retry(), run.maxAttempts),
		Tags:              run.tags,
		Metadata:          string(run.metadata),
		CreatedAtMicros:   now.UnixMicro(),
		DueAtMicros:       run.dueAt.UnixMicro(),
		Message:           messageID,
		PublishedAtMicros: now.UnixMicro(),
		Key:               run.key,
		Lane:              string(runs.LaneFor(deployed.consumer.Lanes, run.lane)),
		Delivery:          token,
	}
	if run.ttl > 0 {
		item.RunExpiresAtMicros = run.dueAt.Add(run.ttl).UnixMicro()
	}
	if err := e.store.putRun(ctx, item); err != nil {
		return err
	}
	msg := queueMessage{Execution: execution, Delivery: token, Key: run.key}
	if err := e.enqueue(ctx, deployed, msg, run.dueAt.Sub(now)); err != nil {
		_, _ = e.store.deleteRun(ctx, deployed.topicName, execution, "", nil)
		return err
	}
	return nil
}

func (t Tasks) debounce(ctx context.Context, deployed deployedConsumer, key, execution string, due time.Time) (string, error) {
	store := t.engine.store
	record := provider.ExpiringRecord{Purpose: provider.RecordDebounce, Topic: deployed.topicName, Key: key, Value: runRecord(execution), ExpiresAt: due}
	held, created, err := store.EnsureRecord(ctx, record)
	if err != nil || created {
		return "", err
	}
	pending := readRecordedRun(held.Value)
	item, err := store.updateRun(ctx, deployed.topicName, pending, change{
		set:        map[string]any{"due_at": due.UnixMicro(), "expires_at": expiresAtUnixAfter(due.UnixMicro())},
		condition:  "#status = :delayed",
		conditions: map[string]any{":delayed": string(provider.RunDelayed)},
	})
	if err == nil {
		if item.RunExpiresAtMicros > 0 {
			ttl := deployed.topic.TTL
			if ttl > 0 {
				if _, err := store.updateRun(ctx, deployed.topicName, pending, change{set: map[string]any{"run_expires_at": due.Add(ttl).UnixMicro()}}); err != nil {
					return "", err
				}
			}
		}
		record.Value = runRecord(pending)
		return pending, store.rewriteRecord(ctx, record)
	}
	if !errors.Is(err, errConditionFailed) {
		return "", err
	}
	return "", store.rewriteRecord(ctx, record)
}

var messageIDLength = len(envelope.NewMessageID(time.Time{}))

func taskOf(id string) (string, bool) {
	if len(id) <= messageIDLength+1 || id[messageIDLength] != '-' {
		return "", false
	}
	return id[messageIDLength+1:], true
}

func (t Tasks) readTaskRun(ctx context.Context, id string) (runItem, error) {
	task, ok := taskOf(id)
	if ok {
		item, found, err := t.engine.store.readRunItem(ctx, task, id, true)
		if err != nil {
			return runItem{}, err
		}
		if found {
			return item, nil
		}
	}
	return runItem{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("no run %q", id))
}

func (t Tasks) RetrieveRun(ctx context.Context, req *taskv1.RetrieveRunRequest) (*taskv1.RetrieveRunResponse, error) {
	item, err := t.readTaskRun(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	return &taskv1.RetrieveRunResponse{Run: runs.NewRunMessage(item.run())}, nil
}

func isUnfinished(status provider.RunStatus) bool {
	return status == provider.RunDelayed || status == provider.RunQueued || status == provider.RunExecuting
}

func (t Tasks) CancelRun(ctx context.Context, req *taskv1.CancelRunRequest) (*taskv1.CancelRunResponse, error) {
	item, err := t.readTaskRun(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	if isUnfinished(provider.RunStatus(item.Status)) {
		now := time.Now()
		canceled, err := t.engine.store.updateRun(ctx, item.Topic, item.SK, change{
			set:        map[string]any{"status": string(provider.RunCanceled), "finished_at": now.UnixMicro(), "expires_at": now.Add(runRetention).Unix()},
			condition:  "#status IN (:delayed, :queued, :executing)",
			conditions: map[string]any{":delayed": string(provider.RunDelayed), ":queued": string(provider.RunQueued), ":executing": string(provider.RunExecuting)},
		})
		switch {
		case err == nil:
			item = canceled
		case errors.Is(err, errConditionFailed):
			if item, err = t.readTaskRun(ctx, req.GetId()); err != nil {
				return nil, err
			}
		default:
			return nil, err
		}
	}
	return &taskv1.CancelRunResponse{Run: runs.NewRunMessage(item.run())}, nil
}

func (t Tasks) RescheduleRun(ctx context.Context, req *taskv1.RescheduleRunRequest) (*taskv1.RescheduleRunResponse, error) {
	item, err := t.readTaskRun(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	if provider.RunStatus(item.Status) != provider.RunDelayed {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("run %q is %s, and only a delayed run can be rescheduled", req.GetId(), item.Status))
	}
	deployed, err := t.task(item.Topic)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	due := req.GetDueAt().AsTime()
	status := provider.RunQueued
	if due.After(now) {
		status = provider.RunDelayed
	}
	set := map[string]any{"status": string(status), "due_at": due.UnixMicro(), "expires_at": expiresAtUnixAfter(due.UnixMicro())}
	token := item.Delivery
	if !deployed.fifo() {
		token = envelope.NewMessageID(now)
		set["delivery"] = token
	}
	if item.RunExpiresAtMicros > 0 {
		set["run_expires_at"] = item.RunExpiresAtMicros + due.UnixMicro() - item.DueAtMicros
	}
	moved, err := t.engine.store.updateRun(ctx, item.Topic, item.SK, change{
		set:        set,
		condition:  "#status = :delayed AND #delivery = :token",
		conditions: map[string]any{":delayed": string(provider.RunDelayed), ":token": item.Delivery},
	})
	if errors.Is(err, errConditionFailed) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("run %q changed while it was being rescheduled; read it and try again", req.GetId()))
	}
	if err != nil {
		return nil, err
	}
	if deployed.fifo() {
		if item.Receipt != "" {
			if err := t.engine.releaseHeld(ctx, deployed, item.Receipt); err != nil {
				slog.Warn("release the held message of a rescheduled run", "execution", item.SK, "error", err)
			}
		}
	} else if err := t.engine.enqueue(ctx, deployed, queueMessage{Execution: item.SK, Delivery: token, Key: item.Key}, due.Sub(now)); err != nil {
		return nil, err
	}
	return &taskv1.RescheduleRunResponse{Run: runs.NewRunMessage(moved.run())}, nil
}

func (t Tasks) ReplayRun(ctx context.Context, req *taskv1.ReplayRunRequest) (*taskv1.ReplayRunResponse, error) {
	item, err := t.readTaskRun(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	deployed, err := t.task(item.Topic)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	run := runRequest{
		payload:     json.RawMessage(item.Payload),
		dueAt:       now,
		key:         item.Key,
		lane:        laneOf(item.Lane),
		maxAttempts: int32(item.MaxAttempts),
		ttl:         deployed.topic.TTL,
		tags:        item.Tags,
		metadata:    json.RawMessage(item.Metadata),
	}
	if item.RunExpiresAtMicros > 0 && item.DueAtMicros > 0 {
		run.ttl = time.Duration(item.RunExpiresAtMicros-item.DueAtMicros) * time.Microsecond
	}
	messageID := envelope.NewMessageID(now)
	execution := runs.ExecutionOf(messageID, deployed.consumer.Name)
	if err := t.engine.startRun(ctx, deployed, messageID, now, execution, run); err != nil {
		return nil, err
	}
	return &taskv1.ReplayRunResponse{Id: execution}, nil
}

func laneOf(name string) topicv1.Lane {
	for _, lane := range []topicv1.Lane{topicv1.Lane_LANE_HIGH, topicv1.Lane_LANE_DEFAULT, topicv1.Lane_LANE_LOW} {
		if string(runs.LaneOf(lane)) == name {
			return lane
		}
	}
	return topicv1.Lane_LANE_UNSPECIFIED
}
