package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/topic/v1/topicv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
)

var _ topicv1connect.TopicServiceHandler = Topics{}

type Topics struct {
	engine *Engine
}

func (e *Engine) Topics() Topics { return Topics{engine: e} }

type recordedMessage struct {
	Message string `json:"message"`
}

func (t Topics) Send(ctx context.Context, req *topicv1.SendRequest) (*topicv1.SendResponse, error) {
	topic, found := t.engine.current().Topics[req.GetTopic()]
	if !found || IsTask(topic) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no topic named %q is deployed", req.GetTopic()))
	}
	if err := RefuseNonJSON(req.GetPayload()); err != nil {
		return nil, err
	}
	now := time.Now()
	msg := Message{
		Topic:       req.GetTopic(),
		ID:          NewMessageID(now),
		PublishedAt: now,
		DueAt:       now,
		Key:         req.GetKey(),
		Payload:     req.GetPayload(),
	}
	if req.GetLane() != topicv1.Lane_LANE_UNSPECIFIED {
		msg.Lane = LaneOf(req.GetLane())
	}
	if req.GetDueAt() != nil {
		msg.DueAt = req.GetDueAt().AsTime()
	}
	if err := t.engine.refuseDelay(req.GetTopic(), topic, msg.DueAt, now); err != nil {
		return nil, err
	}
	var idempotency provider.ExpiringRecord
	if key := req.GetIdempotencyKey(); key != "" {
		value, err := json.Marshal(recordedMessage{Message: msg.ID})
		if err != nil {
			return nil, err
		}
		idempotency = provider.ExpiringRecord{Purpose: provider.RecordIdempotency, Topic: req.GetTopic(), Key: key, Value: value, ExpiresAt: now.Add(provider.DefaultIdempotencyKeyLife)}
		held, created, err := t.engine.cfg.Store.EnsureRecord(ctx, idempotency)
		if err != nil {
			return nil, err
		}
		if !created {
			var recorded recordedMessage
			if err := json.Unmarshal(held.Value, &recorded); err != nil {
				return nil, fmt.Errorf("read the message idempotency key %q names: %w", key, err)
			}
			return &topicv1.SendResponse{MessageId: recorded.Message}, nil
		}
	}
	if err := t.engine.cfg.Queue.Send(ctx, msg); err != nil {
		if idempotency.Key != "" {
			err = errors.Join(err, t.engine.cfg.Store.DeleteRecord(context.WithoutCancel(ctx), idempotency))
		}
		return nil, err
	}
	return &topicv1.SendResponse{MessageId: msg.ID}, nil
}

func (t Topics) consumer(topicName, consumer string) (deployedConsumer, error) {
	deployed, found := t.engine.consumer(topicName, consumer)
	if !found || deployed.isTask() {
		return deployedConsumer{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("topic %q has no consumer %q deployed", topicName, consumer))
	}
	return deployed, nil
}

func deadLettersOf(topic, consumer string) RunFilter {
	return RunFilter{Topics: []string{topic}, Consumer: consumer, Statuses: []provider.RunStatus{provider.RunFailed}}
}

func (t Topics) ListDeadLetters(ctx context.Context, req *topicv1.ListDeadLettersRequest) (*topicv1.ListDeadLettersResponse, error) {
	filter := deadLettersOf(req.GetTopic(), req.GetConsumer())
	filter.Cursor, filter.Limit = req.GetCursor(), int(req.GetLimit())
	page, err := t.engine.cfg.Store.ListRuns(ctx, filter)
	if errors.Is(err, ErrUnknownCursor) {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err != nil {
		return nil, err
	}
	resp := &topicv1.ListDeadLettersResponse{NextCursor: page.NextCursor}
	for _, run := range page.Runs {
		resp.DeadLetters = append(resp.DeadLetters, &topicv1.DeadLetter{
			Execution: run.Execution,
			Message:   &topicv1.Message{Id: run.Message, PublishedAt: TimestampOf(run.PublishedAt)},
			Payload:   run.Payload,
			Attempts:  int32(run.Attempts),
			Error:     run.Error,
			FailedAt:  TimestampOf(run.FinishedAt),
		})
	}
	return resp, nil
}

func (t Topics) eachDeadLetter(ctx context.Context, topic, consumer string, executions []string, each func(Run) error) error {
	if len(executions) > 0 {
		for _, execution := range executions {
			run, err := t.engine.cfg.Store.ReadRun(ctx, topic, execution)
			if errors.Is(err, records.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if run.Consumer != consumer || run.Status != provider.RunFailed {
				continue
			}
			if err := each(run); err != nil {
				return err
			}
		}
		return nil
	}
	filter := deadLettersOf(topic, consumer)
	for {
		page, err := t.engine.cfg.Store.ListRuns(ctx, filter)
		if err != nil {
			return err
		}
		for _, run := range page.Runs {
			if err := each(run); err != nil {
				return err
			}
		}
		if page.NextCursor == "" {
			return nil
		}
		filter.Cursor = page.NextCursor
	}
}

func (t Topics) RedriveDeadLetters(ctx context.Context, req *topicv1.RedriveDeadLettersRequest) (*topicv1.RedriveDeadLettersResponse, error) {
	if _, err := t.consumer(req.GetTopic(), req.GetConsumer()); err != nil {
		return nil, err
	}
	var redriven int64
	err := t.eachDeadLetter(ctx, req.GetTopic(), req.GetConsumer(), req.GetExecutions(), func(letter Run) error {
		now := time.Now()
		again := letter
		again.Status, again.Attempts, again.Error, again.DueAt = provider.RunQueued, 0, "", now
		again.StartedAt, again.FinishedAt, again.LeaseUntil, again.Held = time.Time{}, time.Time{}, time.Time{}, ""
		again.Delivery = NewMessageID(now)
		written, err := t.engine.write(ctx, again)
		if errors.Is(err, records.ErrStale) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := t.engine.cfg.Queue.Send(ctx, messageOf(written)); err != nil {
			letter.Revision = written.Revision
			_, restoreErr := t.engine.write(context.WithoutCancel(ctx), letter)
			return errors.Join(err, restoreErr)
		}
		redriven++
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &topicv1.RedriveDeadLettersResponse{Redriven: redriven}, nil
}

func (t Topics) PurgeDeadLetters(ctx context.Context, req *topicv1.PurgeDeadLettersRequest) (*topicv1.PurgeDeadLettersResponse, error) {
	var purged int64
	err := t.eachDeadLetter(ctx, req.GetTopic(), req.GetConsumer(), req.GetExecutions(), func(letter Run) error {
		err := t.engine.cfg.Store.DeleteRun(ctx, letter)
		if errors.Is(err, records.ErrStale) {
			return nil
		}
		if err == nil {
			purged++
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return &topicv1.PurgeDeadLettersResponse{Purged: purged}, nil
}

func (t Topics) CountDeadLetters(ctx context.Context, req *topicv1.CountDeadLettersRequest) (*topicv1.CountDeadLettersResponse, error) {
	count, err := t.engine.cfg.Store.CountRuns(ctx, deadLettersOf(req.GetTopic(), req.GetConsumer()))
	if err != nil {
		return nil, err
	}
	return &topicv1.CountDeadLettersResponse{Count: count}, nil
}
