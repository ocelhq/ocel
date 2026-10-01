package topics

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
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
	current, err := t.deployment.Store().readRecord(ctx, req.GetId())
	if errors.Is(err, keyvalue.ErrNotFound) {
		return nil, noRun(req.GetId())
	}
	if err != nil {
		return nil, err
	}
	topic, err := t.task(current.Topic)
	if err != nil {
		return nil, err
	}
	if err := refuseUnreschedulable(current, time.Now()); err != nil {
		return nil, err
	}
	moved := publicationOf(current, topic)
	moved.dueAt = req.GetDueAt().AsTime()
	delayTask := t.deployment.delayTaskOf(moved)
	if err := t.deployment.publish(ctx, moved, delayTask); err != nil {
		return nil, err
	}
	_, err = t.deployment.Store().changeRun(ctx, req.GetId(), func(record *runRecord, found bool) error {
		if !found || record.delivery.DelayTask != current.delivery.DelayTask {
			return connect.NewError(connect.CodeAborted, fmt.Errorf("run %q changed while it was rescheduled", req.GetId()))
		}
		if err := refuseUnreschedulable(*record, time.Now()); err != nil {
			return err
		}
		record.DueAt, record.delivery.DelayTask, record.Status = moved.dueAt, delayTask, provider.RunDelayed
		if delayTask == "" {
			record.Status = provider.RunQueued
		}
		return nil
	})
	if err != nil {
		return nil, errors.Join(err, t.deployment.unschedule(ctx, delayTask))
	}
	if err := t.deployment.unschedule(ctx, current.delivery.DelayTask); err != nil {
		return nil, err
	}
	resp, err := t.RetrieveRun(ctx, &taskv1.RetrieveRunRequest{Id: req.GetId()})
	if err != nil {
		return nil, err
	}
	return &taskv1.RescheduleRunResponse{Run: resp.GetRun()}, nil
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
