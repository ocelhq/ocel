package ocel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
	taskv1 "ocel.dev/internal/proto/app/task/v1"
	"ocel.dev/internal/proto/app/task/v1/taskv1connect"
)

// A RunStatus is where a run is in its life.
type RunStatus string

const (
	// RunDelayed is a run that is not yet due.
	RunDelayed RunStatus = "DELAYED"
	// RunQueued is a run that is due and waits for a worker.
	RunQueued RunStatus = "QUEUED"
	// RunExecuting is a run an attempt is running.
	RunExecuting RunStatus = "EXECUTING"
	// RunCompleted is a run that succeeded.
	RunCompleted RunStatus = "COMPLETED"
	// RunFailed is a run that was aborted or ran out of attempts.
	RunFailed RunStatus = "FAILED"
	// RunCanceled is a run [CancelRun] stopped.
	RunCanceled RunStatus = "CANCELED"
	// RunExpired is a run that waited longer than its [TTL] to start.
	RunExpired RunStatus = "EXPIRED"
	// RunTimedOut is a run whose attempt ran longer than its task's
	// [MaxDuration].
	RunTimedOut RunStatus = "TIMED_OUT"
)

func (s RunStatus) encode() (taskv1.RunStatus, error) {
	encoded, ok := taskv1.RunStatus_value["RUN_STATUS_"+string(s)]
	if !ok || encoded == int32(taskv1.RunStatus_RUN_STATUS_UNSPECIFIED) {
		return 0, fmt.Errorf("run status %q is not one a run can be in", string(s))
	}
	return taskv1.RunStatus(encoded), nil
}

// A Run is the record of one run of a task.
type Run struct {
	// ID is the run's id.
	ID string
	// Task is the bound name of the task the run belongs to.
	Task string
	// Status is where the run is in its life.
	Status RunStatus
	// Payload is what the run was triggered with, as JSON.
	Payload json.RawMessage
	// Output is what the run returned, as JSON, once it has completed.
	Output json.RawMessage
	// Error is what the run failed with, once it has failed.
	Error string
	// Attempts is how many attempts the run has made.
	Attempts int
	// Tags are the run's [Tags].
	Tags []string
	// Metadata is the run's [RunMetadata].
	Metadata map[string]any
	// CreatedAt is when the run was triggered.
	CreatedAt time.Time
	// DueAt is when the run is or was due.
	DueAt time.Time
	// StartedAt is when the run's first attempt started.
	StartedAt time.Time
	// FinishedAt is when the run ended.
	FinishedAt time.Time
	// ExpiresAt is when the run expires if it has not started.
	ExpiresAt time.Time
}

// A RunPage is one page of a [ListRuns] listing.
type RunPage struct {
	// Runs are the runs on this page.
	Runs []*Run
	// NextCursor reads the next page when passed to [Cursor], and is empty on
	// the last page.
	NextCursor string
}

// A RunListOption narrows or pages through a [ListRuns] listing.
type RunListOption interface {
	applyRunList(*runListSettings)
}

// A PageOption pages through a run or a dead-letter listing alike.
type PageOption interface {
	RunListOption
	DeadLetterListOption
}

type pageSettings struct {
	cursor string
	limit  int32
}

type runListSettings struct {
	pageSettings
	task     string
	statuses []RunStatus
	tags     []string
}

// ForTask lists only the runs of the task declared under name.
func ForTask(name string) RunListOption {
	return option{runList: func(s *runListSettings) { s.task = name }}
}

// Statuses lists only the runs in one of statuses.
func Statuses(statuses ...RunStatus) RunListOption {
	return option{runList: func(s *runListSettings) { s.statuses = statuses }}
}

// Cursor reads the page a previous page's NextCursor points at.
func Cursor(cursor string) PageOption {
	return option{
		runList:        func(s *runListSettings) { s.cursor = cursor },
		deadLetterList: func(s *deadLetterListSettings) { s.cursor = cursor },
	}
}

// RetrieveRun reads the run with id.
func RetrieveRun(ctx context.Context, id string) (*Run, error) {
	client, err := newRunsClient("RetrieveRun")
	if err != nil {
		return nil, err
	}
	res, err := client.RetrieveRun(ctx, &taskv1.RetrieveRunRequest{Id: id})
	if err != nil {
		return nil, err
	}
	return decodeRun(res.GetRun())
}

// ListRuns reads one page of runs, narrowed by opts.
func ListRuns(ctx context.Context, opts ...RunListOption) (*RunPage, error) {
	client, err := newRunsClient("ListRuns")
	if err != nil {
		return nil, err
	}
	settings := runListSettings{}
	for _, opt := range opts {
		opt.applyRunList(&settings)
	}
	req := &taskv1.ListRunsRequest{Tags: settings.tags, Cursor: settings.cursor, Limit: settings.limit}
	if settings.task != "" {
		task, err := dialTask(settings.task)
		if err != nil {
			return nil, err
		}
		req.Task = task.name
	}
	for _, status := range settings.statuses {
		encoded, err := status.encode()
		if err != nil {
			return nil, err
		}
		req.Statuses = append(req.Statuses, encoded)
	}
	res, err := client.ListRuns(ctx, req)
	if err != nil {
		return nil, err
	}
	page := &RunPage{NextCursor: res.GetNextCursor()}
	for _, wire := range res.GetRuns() {
		run, err := decodeRun(wire)
		if err != nil {
			return nil, err
		}
		page.Runs = append(page.Runs, run)
	}
	return page, nil
}

// CancelRun stops the run with id from making further attempts, and cancels
// the context of the attempt running now on a best-effort basis.
func CancelRun(ctx context.Context, id string) (*Run, error) {
	client, err := newRunsClient("CancelRun")
	if err != nil {
		return nil, err
	}
	res, err := client.CancelRun(ctx, &taskv1.CancelRunRequest{Id: id})
	if err != nil {
		return nil, err
	}
	return decodeRun(res.GetRun())
}

// ReplayRun starts a new run with the payload and options of the run with
// id, and returns the new run's handle.
func ReplayRun(ctx context.Context, id string) (RunHandle, error) {
	client, err := newRunsClient("ReplayRun")
	if err != nil {
		return RunHandle{}, err
	}
	res, err := client.ReplayRun(ctx, &taskv1.ReplayRunRequest{Id: id})
	if err != nil {
		return RunHandle{}, err
	}
	return RunHandle{ID: res.GetId()}, nil
}

// RescheduleRun moves when the delayed run with id is due, given as [Delay]
// or [DelayUntil].
func RescheduleRun(ctx context.Context, id string, due DelayOption) (*Run, error) {
	client, err := newRunsClient("RescheduleRun")
	if err != nil {
		return nil, err
	}
	res, err := client.RescheduleRun(ctx, &taskv1.RescheduleRunRequest{
		Id:    id,
		DueAt: timestamppb.New(due.computeDueAt(time.Now())),
	})
	if err != nil {
		return nil, err
	}
	return decodeRun(res.GetRun())
}

func newRunsClient(access string) (taskv1connect.TaskServiceClient, error) {
	if discovering() {
		return nil, &UnprovisionedError{Resource: "runs", Access: access}
	}
	return newTaskClient()
}

func decodeRun(wire *taskv1.Run) (*Run, error) {
	if wire == nil {
		return nil, fmt.Errorf("the runtime answered with no run")
	}
	payload, err := encodeValueAsJSON(wire.GetPayload())
	if err != nil {
		return nil, err
	}
	output, err := encodeValueAsJSON(wire.GetOutput())
	if err != nil {
		return nil, err
	}
	run := &Run{
		ID:         wire.GetId(),
		Task:       wire.GetTask(),
		Payload:    payload,
		Output:     output,
		Error:      wire.GetError(),
		Attempts:   int(wire.GetAttempts()),
		Tags:       wire.GetTags(),
		CreatedAt:  decodeTimestamp(wire.GetCreatedAt()),
		DueAt:      decodeTimestamp(wire.GetDueAt()),
		StartedAt:  decodeTimestamp(wire.GetStartedAt()),
		FinishedAt: decodeTimestamp(wire.GetFinishedAt()),
		ExpiresAt:  decodeTimestamp(wire.GetExpiresAt()),
	}
	if status := wire.GetStatus(); status != taskv1.RunStatus_RUN_STATUS_UNSPECIFIED {
		run.Status = RunStatus(strings.TrimPrefix(status.String(), "RUN_STATUS_"))
	}
	if metadata := wire.GetMetadata(); metadata != nil {
		run.Metadata = metadata.AsMap()
	}
	return run, nil
}
