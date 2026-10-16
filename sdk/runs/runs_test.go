package runs_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
	"ocel.dev"
	taskv1 "ocel.dev/internal/proto/app/task/v1"
	"ocel.dev/internal/sdktest"
	"ocel.dev/runs"
)

var createdAt = time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)

func storedRun() *taskv1.Run {
	return &taskv1.Run{
		Id:         "run-1",
		Task:       "resize",
		Status:     taskv1.RunStatus_RUN_STATUS_TIMED_OUT,
		Payload:    []byte(`{"key":"a.png","id":9007199254740993}`),
		Output:     []byte(`2.0`),
		Error:      "the attempt ran past its max duration",
		Attempts:   1,
		Tags:       []string{"user:1"},
		Metadata:   []byte(`{"source":"upload"}`),
		CreatedAt:  timestamppb.New(createdAt),
		DueAt:      timestamppb.New(createdAt.Add(time.Minute)),
		StartedAt:  timestamppb.New(createdAt.Add(2 * time.Minute)),
		FinishedAt: timestamppb.New(createdAt.Add(3 * time.Minute)),
		ExpiresAt:  timestamppb.New(createdAt.Add(time.Hour)),
	}
}

func TestRetrieveReadsTheRunRecord(t *testing.T) {
	runtime := sdktest.ServeRuntime(t, nil)
	runtime.Run = storedRun()

	run, err := runs.Retrieve(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}

	if req := sdktest.Only[*taskv1.RetrieveRunRequest](t, runtime); req.GetId() != "run-1" {
		t.Errorf("id = %q", req.GetId())
	}
	if run.ID != "run-1" || run.Task != "resize" || run.Status != runs.TimedOut ||
		string(run.Payload) != `{"key":"a.png","id":9007199254740993}` || string(run.Output) != "2.0" ||
		run.Error != "the attempt ran past its max duration" || run.Attempts != 1 ||
		!slices.Equal(run.Tags, []string{"user:1"}) || string(run.Metadata) != `{"source":"upload"}` {
		t.Errorf("run = %+v", run)
	}
	if !run.CreatedAt.Equal(createdAt) || !run.DueAt.Equal(createdAt.Add(time.Minute)) ||
		!run.StartedAt.Equal(createdAt.Add(2*time.Minute)) || !run.FinishedAt.Equal(createdAt.Add(3*time.Minute)) ||
		!run.ExpiresAt.Equal(createdAt.Add(time.Hour)) {
		t.Errorf("run times = %+v", run)
	}
}

func TestListNarrowsByTheDeclaredTaskStatusesAndTags(t *testing.T) {
	runtime := sdktest.ServeRuntime(t, sdktest.TaskBinding("resize-listed"))
	runtime.Run = storedRun()

	page, err := runs.List(t.Context(),
		runs.ForTask("resize-listed"),
		runs.Statuses(runs.Failed, runs.TimedOut),
		runs.Tags("user:1"),
		runs.Cursor("c-1"),
		runs.Limit(50),
	)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	req := sdktest.Only[*taskv1.ListRunsRequest](t, runtime)
	if req.GetTask() != "resize-listed" || req.GetCursor() != "c-1" || req.GetLimit() != 50 ||
		!slices.Equal(req.GetTags(), []string{"user:1"}) ||
		!slices.Equal(req.GetStatuses(), []taskv1.RunStatus{taskv1.RunStatus_RUN_STATUS_FAILED, taskv1.RunStatus_RUN_STATUS_TIMED_OUT}) {
		t.Errorf("request = %v", req)
	}
	if page.NextCursor != "after-runs" || len(page.Runs) != 1 || page.Runs[0].ID != "run-1" {
		t.Errorf("page = %+v", page)
	}
}

func TestCancelCancelsTheRunAndReturnsItsRecord(t *testing.T) {
	runtime := sdktest.ServeRuntime(t, nil)
	runtime.Run = storedRun()

	run, err := runs.Cancel(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}

	if req := sdktest.Only[*taskv1.CancelRunRequest](t, runtime); req.GetId() != "run-1" || run.ID != "run-1" {
		t.Errorf("Cancel() = %+v after %v", run, req)
	}
}

func TestReplayReturnsTheNewRunsHandle(t *testing.T) {
	runtime := sdktest.ServeRuntime(t, nil)

	run, err := runs.Replay(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}

	if req := sdktest.Only[*taskv1.ReplayRunRequest](t, runtime); req.GetId() != "run-1" || run != (runs.Handle{ID: "run-2"}) {
		t.Errorf("Replay() = %+v after %v", run, req)
	}
}

func TestRescheduleMovesTheRunDueByADelayFromNow(t *testing.T) {
	runtime := sdktest.ServeRuntime(t, nil)
	runtime.Run = storedRun()

	before := time.Now()
	if _, err := runs.Reschedule(t.Context(), "run-1", runs.Delay(10*time.Minute)); err != nil {
		t.Fatalf("Reschedule() error = %v", err)
	}

	req := sdktest.Only[*taskv1.RescheduleRunRequest](t, runtime)
	due := req.GetDueAt().AsTime()
	if req.GetId() != "run-1" || due.Before(before.Add(10*time.Minute)) || due.After(time.Now().Add(10*time.Minute)) {
		t.Errorf("request = %v, want the run due ten minutes from now", req)
	}
}

func TestRunOperationsRefuseDuringDiscovery(t *testing.T) {
	sdktest.Declarations(t)

	for access, err := range map[string]error{
		"Retrieve":   sdktest.ErrorOf(runs.Retrieve(t.Context(), "run-1")),
		"List":       sdktest.ErrorOf(runs.List(t.Context())),
		"Cancel":     sdktest.ErrorOf(runs.Cancel(t.Context(), "run-1")),
		"Replay":     sdktest.ErrorOf(runs.Replay(t.Context(), "run-1")),
		"Reschedule": sdktest.ErrorOf(runs.Reschedule(t.Context(), "run-1", runs.Delay(time.Minute))),
	} {
		var unprovisioned *ocel.UnprovisionedError
		if !errors.As(err, &unprovisioned) || unprovisioned.Resource != "runs" || unprovisioned.Access != access {
			t.Errorf("%s error = %v, want an *UnprovisionedError naming it", access, err)
		}
	}
}

func TestListRefusesAStatusItDoesNotKnow(t *testing.T) {
	runtime := sdktest.ServeRuntime(t, nil)

	_, err := runs.List(t.Context(), runs.Statuses(runs.Failed, "RUNNING"))

	if err == nil || !strings.Contains(err.Error(), `"RUNNING"`) || len(runtime.Requests()) != 0 {
		t.Errorf("List() error = %v after %d requests, want an error naming the status and no request", err, len(runtime.Requests()))
	}
}
