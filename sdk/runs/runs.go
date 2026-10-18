// Package runs reads and steers the runs an app's tasks start, and tells a
// running task or consumer which run or message it is handling.
package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
	"ocel.dev"
	"ocel.dev/internal/binding"
	"ocel.dev/internal/codec"
	"ocel.dev/internal/declaration"
	taskv1 "ocel.dev/internal/proto/app/task/v1"
	"ocel.dev/internal/proto/app/task/v1/taskv1connect"
)

// A Status is where a run is in its life.
type Status string

const (
	// Delayed is a run that is not yet due.
	Delayed Status = "DELAYED"
	// Queued is a run that is due and waits for a worker.
	Queued Status = "QUEUED"
	// Executing is a run an attempt is running.
	Executing Status = "EXECUTING"
	// Completed is a run that succeeded.
	Completed Status = "COMPLETED"
	// Failed is a run that was aborted or ran out of attempts.
	Failed Status = "FAILED"
	// Canceled is a run [Cancel] stopped.
	Canceled Status = "CANCELED"
	// Expired is a run that waited longer than its TTL to start.
	Expired Status = "EXPIRED"
	// TimedOut is a run whose attempt ran longer than its task's
	// MaxDuration.
	TimedOut Status = "TIMED_OUT"
)

func (s Status) encode() (taskv1.RunStatus, error) {
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
	// Task is the name the run's task was declared under.
	Task string
	// Status is where the run is in its life.
	Status Status
	// Payload is what the run was triggered with, as JSON.
	Payload json.RawMessage
	// Output is what the run returned, as JSON, once it has completed.
	Output json.RawMessage
	// Error is what the run failed with, once it has failed.
	Error string
	// Attempts is how many attempts the run has made.
	Attempts int
	// Tags are the run's tags, given at trigger.
	Tags []string
	// Metadata is the run's metadata, given at trigger, as a JSON object.
	Metadata json.RawMessage
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

// A Handle is a run a trigger started.
type Handle struct {
	// ID is the run's id, which [Retrieve] and the other functions of this
	// package take.
	ID string
}

// A Page is one page of a [List] listing.
type Page struct {
	// Runs are the runs on this page.
	Runs []*Run
	// NextCursor reads the next page when passed to [Cursor], and is empty on
	// the last page.
	NextCursor string
}

// A ListOption narrows or pages through a [List] listing.
type ListOption interface {
	applyList(*listSettings)
}

type listSettings struct {
	cursor   string
	limit    int32
	task     string
	statuses []Status
	tags     []string
}

type listOption func(*listSettings)

func (o listOption) applyList(s *listSettings) { o(s) }

// ForTask lists only the runs of the task declared under name.
func ForTask(name string) ListOption {
	return listOption(func(s *listSettings) { s.task = name })
}

// Statuses lists only the runs in one of statuses.
func Statuses(statuses ...Status) ListOption {
	return listOption(func(s *listSettings) { s.statuses = statuses })
}

// Tags lists only the runs carrying every tag.
func Tags(tags ...string) ListOption {
	return listOption(func(s *listSettings) { s.tags = tags })
}

// Limit is how many runs a page holds at most.
func Limit(runs int) ListOption {
	return listOption(func(s *listSettings) { s.limit = int32(runs) })
}

// Cursor reads the page a previous page's NextCursor points at.
func Cursor(cursor string) ListOption {
	return listOption(func(s *listSettings) { s.cursor = cursor })
}

// Retrieve reads the run with id.
func Retrieve(ctx context.Context, id string) (*Run, error) {
	client, err := newClient("Retrieve")
	if err != nil {
		return nil, err
	}
	res, err := client.RetrieveRun(ctx, &taskv1.RetrieveRunRequest{Id: id})
	if err != nil {
		return nil, err
	}
	return decodeRun(res.GetRun())
}

// List reads one page of runs, narrowed by opts.
func List(ctx context.Context, opts ...ListOption) (*Page, error) {
	client, err := newClient("List")
	if err != nil {
		return nil, err
	}
	settings := listSettings{}
	for _, opt := range opts {
		opt.applyList(&settings)
	}
	req := &taskv1.ListRunsRequest{Task: settings.task, Tags: settings.tags, Cursor: settings.cursor, Limit: settings.limit}
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
	page := &Page{NextCursor: res.GetNextCursor()}
	for _, wire := range res.GetRuns() {
		run, err := decodeRun(wire)
		if err != nil {
			return nil, err
		}
		page.Runs = append(page.Runs, run)
	}
	return page, nil
}

// Cancel stops the run with id from making further attempts, and cancels
// the context of the attempt running now on a best-effort basis.
func Cancel(ctx context.Context, id string) (*Run, error) {
	client, err := newClient("Cancel")
	if err != nil {
		return nil, err
	}
	res, err := client.CancelRun(ctx, &taskv1.CancelRunRequest{Id: id})
	if err != nil {
		return nil, err
	}
	return decodeRun(res.GetRun())
}

// Replay starts a new run with the payload and options of the run with id,
// and returns the new run's handle.
func Replay(ctx context.Context, id string) (Handle, error) {
	client, err := newClient("Replay")
	if err != nil {
		return Handle{}, err
	}
	res, err := client.ReplayRun(ctx, &taskv1.ReplayRunRequest{Id: id})
	if err != nil {
		return Handle{}, err
	}
	return Handle{ID: res.GetId()}, nil
}

// Reschedule makes the delayed run with id due at due, at most 30 days from
// now.
func Reschedule(ctx context.Context, id string, due time.Time) (*Run, error) {
	client, err := newClient("Reschedule")
	if err != nil {
		return nil, err
	}
	res, err := client.RescheduleRun(ctx, &taskv1.RescheduleRunRequest{
		Id:    id,
		DueAt: timestamppb.New(due),
	})
	if err != nil {
		return nil, err
	}
	return decodeRun(res.GetRun())
}

func newClient(access string) (taskv1connect.TaskServiceClient, error) {
	if declaration.Discovering() {
		return nil, &ocel.UnprovisionedError{Resource: "runs", Access: access}
	}
	address, authorized, err := binding.ReadRuntimeConnection()
	if err != nil {
		return nil, err
	}
	return taskv1connect.NewTaskServiceClient(http.DefaultClient, address, authorized), nil
}

func decodeRun(wire *taskv1.Run) (*Run, error) {
	if wire == nil {
		return nil, fmt.Errorf("the runtime answered with no run")
	}
	run := &Run{
		ID:         wire.GetId(),
		Task:       wire.GetTask(),
		Payload:    codec.ReadJSON(wire.GetPayload()),
		Output:     codec.ReadJSON(wire.GetOutput()),
		Metadata:   codec.ReadJSON(wire.GetMetadata()),
		Error:      wire.GetError(),
		Attempts:   int(wire.GetAttempts()),
		Tags:       wire.GetTags(),
		CreatedAt:  codec.DecodeTimestamp(wire.GetCreatedAt()),
		DueAt:      codec.DecodeTimestamp(wire.GetDueAt()),
		StartedAt:  codec.DecodeTimestamp(wire.GetStartedAt()),
		FinishedAt: codec.DecodeTimestamp(wire.GetFinishedAt()),
		ExpiresAt:  codec.DecodeTimestamp(wire.GetExpiresAt()),
	}
	if status := wire.GetStatus(); status != taskv1.RunStatus_RUN_STATUS_UNSPECIFIED {
		run.Status = Status(strings.TrimPrefix(status.String(), "RUN_STATUS_"))
	}
	return run, nil
}
