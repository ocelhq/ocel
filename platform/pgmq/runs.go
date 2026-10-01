package pgmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (t Tasks) BatchTrigger(ctx context.Context, req *taskv1.BatchTriggerRequest) (*taskv1.BatchTriggerResponse, error) {
	topic, err := t.task(req.GetTask())
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(req.GetItems()))
	err = t.engine.inTx(ctx, func(tx pgx.Tx) error {
		for _, item := range req.GetItems() {
			id, err := t.trigger(ctx, tx, req.GetTask(), topic, item.GetPayload(), item.GetOptions())
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	t.engine.signalTopic(req.GetTask(), topic)
	return &taskv1.BatchTriggerResponse{Ids: ids}, nil
}

func (t Tasks) ListRuns(ctx context.Context, req *taskv1.ListRunsRequest) (*taskv1.ListRunsResponse, error) {
	filter := provider.RunFilter{Topic: req.GetTask(), Tags: req.GetTags(), Cursor: req.GetCursor(), Limit: int(req.GetLimit())}
	for _, status := range req.GetStatuses() {
		for stored, wire := range runStatuses {
			if wire == status {
				filter.Statuses = append(filter.Statuses, stored)
			}
		}
	}
	page, err := listRuns(ctx, t.engine.pool, filter)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	resp := &taskv1.ListRunsResponse{NextCursor: page.NextCursor}
	for _, run := range page.Runs {
		resp.Runs = append(resp.Runs, runMessage(run))
	}
	return resp, nil
}

type queuedRun struct {
	status  provider.RunStatus
	queue   string
	message *int64
}

func lockRun(ctx context.Context, tx pgx.Tx, id string) (queuedRun, error) {
	var run queuedRun
	var status string
	err := tx.QueryRow(ctx, "SELECT status, queue, queue_message FROM ocel.runs WHERE execution = $1 FOR UPDATE", id).Scan(&status, &run.queue, &run.message)
	if errors.Is(err, pgx.ErrNoRows) {
		return run, connect.NewError(connect.CodeNotFound, fmt.Errorf("no run %q", id))
	}
	run.status = provider.RunStatus(status)
	return run, err
}

func isUnfinished(status provider.RunStatus) bool {
	return status == provider.RunDelayed || status == provider.RunQueued || status == provider.RunExecuting
}

func (t Tasks) CancelRun(ctx context.Context, req *taskv1.CancelRunRequest) (*taskv1.CancelRunResponse, error) {
	var stop bool
	err := t.engine.inTx(ctx, func(tx pgx.Tx) error {
		run, err := lockRun(ctx, tx, req.GetId())
		if err != nil || !isUnfinished(run.status) {
			return err
		}
		stop = run.status == provider.RunExecuting
		if _, err := tx.Exec(ctx, `UPDATE ocel.runs SET status = 'canceled', finished_at = clock_timestamp(), revision = `+newRevisionSQL+`
			WHERE execution = $1`, req.GetId()); err != nil {
			return err
		}
		if run.message == nil {
			return nil
		}
		_, err = tx.Exec(ctx, "SELECT pgmq.delete($1, $2::bigint)", run.queue, *run.message)
		return err
	})
	if err != nil {
		return nil, err
	}
	if stop {
		t.engine.stopInFlight(req.GetId())
	}
	resp, err := t.RetrieveRun(ctx, &taskv1.RetrieveRunRequest{Id: req.GetId()})
	if err != nil {
		return nil, err
	}
	return &taskv1.CancelRunResponse{Run: resp.GetRun()}, nil
}

func (t Tasks) RescheduleRun(ctx context.Context, req *taskv1.RescheduleRunRequest) (*taskv1.RescheduleRunResponse, error) {
	due := req.GetDueAt().AsTime()
	var queue string
	err := t.engine.inTx(ctx, func(tx pgx.Tx) error {
		run, err := lockRun(ctx, tx, req.GetId())
		if err != nil {
			return err
		}
		if run.status != provider.RunDelayed && run.status != provider.RunQueued {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("run %q is %s, and only a run that has not started can be rescheduled", req.GetId(), run.status))
		}
		status := provider.RunQueued
		if due.After(time.Now()) {
			status = provider.RunDelayed
		}
		if _, err := tx.Exec(ctx, `UPDATE ocel.runs SET status = $2, due_at = $3, revision = `+newRevisionSQL+` WHERE execution = $1`,
			req.GetId(), string(status), due); err != nil {
			return err
		}
		queue = run.queue
		_, err = tx.Exec(ctx, "SELECT pgmq.set_vt($1, $2::bigint, $3::timestamptz)", run.queue, *run.message, due)
		return err
	})
	if err != nil {
		return nil, err
	}
	t.engine.signal(queue)
	resp, err := t.RetrieveRun(ctx, &taskv1.RetrieveRunRequest{Id: req.GetId()})
	if err != nil {
		return nil, err
	}
	return &taskv1.RescheduleRunResponse{Run: resp.GetRun()}, nil
}

func (t Tasks) ReplayRun(ctx context.Context, req *taskv1.ReplayRunRequest) (*taskv1.ReplayRunResponse, error) {
	var task, key, lane string
	var payload, metadata []byte
	var tags []string
	err := t.engine.pool.QueryRow(ctx, "SELECT topic, key, lane, payload, metadata, tags FROM ocel.runs WHERE execution = $1", req.GetId()).
		Scan(&task, &key, &lane, &payload, &metadata, &tags)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no run %q", req.GetId()))
	}
	if err != nil {
		return nil, err
	}
	topic, err := t.task(task)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	p := publication{
		topicName:   task,
		topic:       topic,
		messageID:   newMessageID(now),
		publishedAt: now,
		dueAt:       now,
		payload:     json.RawMessage(payload),
		key:         key,
		lane:        laneOf(lane),
		ttl:         topic.GetTtl().AsDuration(),
		tags:        tags,
		metadata:    json.RawMessage(metadata),
	}
	var executions []string
	err = t.engine.inTx(ctx, func(tx pgx.Tx) error {
		executions, err = t.engine.publish(ctx, tx, p)
		return err
	})
	if err != nil {
		return nil, err
	}
	t.engine.signalTopic(task, topic)
	return &taskv1.ReplayRunResponse{Id: executions[0]}, nil
}

func laneOf(name string) topicv1.Lane {
	for _, lane := range allLanes {
		if laneName(lane) == name {
			return lane
		}
	}
	return topicv1.Lane_LANE_UNSPECIFIED
}
