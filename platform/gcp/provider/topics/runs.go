package topics

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/envelope"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	moveAttempts     = 5
	debounceAttempts = 5
)

func noRun(id string) error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("no run %q", id))
}

func (s Store) readRecord(ctx context.Context, execution string) (runRecord, error) {
	runs, err := s.runs()
	if err != nil {
		return runRecord{}, err
	}
	snapshot, err := runs.Doc(execution).Get(ctx)
	if err == nil && snapshot.Exists() {
		return recordOf(snapshot)
	}
	if err == nil || isNotFound(err) {
		return runRecord{}, keyvalue.ErrNotFound
	}
	return runRecord{}, fmt.Errorf("read run %s: %w", execution, err)
}

func isUnfinished(status provider.RunStatus) bool {
	return status == provider.RunDelayed || status == provider.RunQueued || status == provider.RunExecuting
}

func (t Tasks) CancelRun(ctx context.Context, req *taskv1.CancelRunRequest) (*taskv1.CancelRunResponse, error) {
	var delayTask string
	_, err := t.deployment.Store().changeRun(ctx, req.GetId(), func(record *runRecord, found bool) error {
		if !found {
			return noRun(req.GetId())
		}
		if !isUnfinished(record.Status) {
			return errRunUnchanged
		}
		delayTask = record.delivery.DelayTask
		record.Status, record.FinishedAt, record.delivery.DelayTask = provider.RunCanceled, time.Now(), ""
		return nil
	})
	if err != nil && !errors.Is(err, errRunUnchanged) {
		return nil, err
	}
	if err := t.deployment.unschedule(ctx, delayTask); err != nil {
		return nil, err
	}
	resp, err := t.RetrieveRun(ctx, &taskv1.RetrieveRunRequest{Id: req.GetId()})
	if err != nil {
		return nil, err
	}
	return &taskv1.CancelRunResponse{Run: resp.GetRun()}, nil
}

func (t Tasks) RescheduleRun(ctx context.Context, req *taskv1.RescheduleRunRequest) (*taskv1.RescheduleRunResponse, error) {
	if err := t.move(ctx, req.GetId(), req.GetDueAt().AsTime(), false); err != nil {
		return nil, err
	}
	resp, err := t.RetrieveRun(ctx, &taskv1.RetrieveRunRequest{Id: req.GetId()})
	if err != nil {
		return nil, err
	}
	return &taskv1.RescheduleRunResponse{Run: resp.GetRun()}, nil
}

func (t Tasks) move(ctx context.Context, id string, due time.Time, keepTTL bool) error {
	var err error
	for range moveAttempts {
		if err = t.moveOnce(ctx, id, due, keepTTL); connect.CodeOf(err) != connect.CodeAborted {
			return err
		}
	}
	return err
}

func (t Tasks) moveOnce(ctx context.Context, id string, due time.Time, keepTTL bool) error {
	current, err := t.deployment.Store().readRecord(ctx, id)
	if errors.Is(err, keyvalue.ErrNotFound) {
		return noRun(id)
	}
	if err != nil {
		return err
	}
	topic, err := t.task(current.Topic)
	if err != nil {
		return err
	}
	if err := refuseUnreschedulable(current, time.Now()); err != nil {
		return err
	}
	moved := publicationOf(current, topic)
	moved.dueAt = due
	delayTask := t.deployment.delayTaskOf(moved)
	if err := t.deployment.publish(ctx, moved, delayTask); err != nil {
		return err
	}
	_, err = t.deployment.Store().changeRun(ctx, id, func(record *runRecord, found bool) error {
		if !found || record.delivery.DelayTask != current.delivery.DelayTask {
			return connect.NewError(connect.CodeAborted, fmt.Errorf("run %q changed while it was rescheduled", id))
		}
		if err := refuseUnreschedulable(*record, time.Now()); err != nil {
			return err
		}
		if keepTTL && !record.ExpiresAt.IsZero() {
			record.ExpiresAt = record.ExpiresAt.Add(due.Sub(record.DueAt))
		}
		record.DueAt, record.delivery.DelayTask, record.Status = due, delayTask, provider.RunDelayed
		if delayTask == "" {
			record.Status = provider.RunQueued
		}
		return nil
	})
	if err != nil {
		return errors.Join(err, t.deployment.unschedule(ctx, delayTask))
	}
	return t.deployment.unschedule(ctx, current.delivery.DelayTask)
}

func (t Tasks) ReplayRun(ctx context.Context, req *taskv1.ReplayRunRequest) (*taskv1.ReplayRunResponse, error) {
	original, err := t.deployment.Store().readRecord(ctx, req.GetId())
	if errors.Is(err, keyvalue.ErrNotFound) {
		return nil, noRun(req.GetId())
	}
	if err != nil {
		return nil, err
	}
	topic, err := t.task(original.Topic)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	replay := publicationOf(original, topic)
	replay.messageID, replay.publishedAt, replay.dueAt = envelope.NewMessageID(now), now, now
	ttl := topic.GetTtl().AsDuration()
	if !original.ExpiresAt.IsZero() && !original.DueAt.IsZero() {
		ttl = original.ExpiresAt.Sub(original.DueAt)
	}
	run := t.deployment.newStoredRun(replay, now, ttl, original.Tags, original.Metadata)
	if err := t.start(ctx, replay, run); err != nil {
		return nil, err
	}
	return &taskv1.ReplayRunResponse{Id: run.Execution}, nil
}

func refuseUnreschedulable(record runRecord, now time.Time) error {
	if status := statusAt(record.Run, now); status != provider.RunDelayed {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("run %q is %s, and only a delayed run can be rescheduled", record.Execution, status))
	}
	if record.delivery.DelayTask == "" {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("run %q waits on no delay task, so nothing would run it at a new time", record.Execution))
	}
	return nil
}

func publicationOf(record runRecord, topic *contractv1.ManifestTopic) publication {
	published := publication{
		topicName:   record.Topic,
		topic:       topic,
		messageID:   record.delivery.MessageID,
		dueAt:       record.DueAt,
		payload:     record.Payload,
		key:         record.delivery.Key,
		lane:        record.delivery.Lane,
		maxAttempts: int32(record.delivery.MaxAttempts),
	}
	if record.delivery.PublishedAt != nil {
		published.publishedAt = *record.delivery.PublishedAt
	}
	return published
}
