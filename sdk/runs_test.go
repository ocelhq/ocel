package ocel_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"ocel.dev"
	taskv1 "ocel.dev/internal/proto/app/task/v1"
)

var createdAt = time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)

func storedRun() *taskv1.Run {
	metadata, _ := structpb.NewStruct(map[string]any{"source": "upload"})
	return &taskv1.Run{
		Id:         "run-1",
		Task:       "project-env-resize",
		Status:     taskv1.RunStatus_RUN_STATUS_TIMED_OUT,
		Payload:    structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{"key": structpb.NewStringValue("a.png")}}),
		Output:     structpb.NewNullValue(),
		Error:      "the attempt ran past its max duration",
		Attempts:   1,
		Tags:       []string{"user:1"},
		Metadata:   metadata,
		CreatedAt:  timestamppb.New(createdAt),
		DueAt:      timestamppb.New(createdAt.Add(time.Minute)),
		StartedAt:  timestamppb.New(createdAt.Add(2 * time.Minute)),
		FinishedAt: timestamppb.New(createdAt.Add(3 * time.Minute)),
		ExpiresAt:  timestamppb.New(createdAt.Add(time.Hour)),
	}
}

func TestRetrieveRunReadsTheRunRecord(t *testing.T) {
	runtime := serveRuntime(t, nil)
	runtime.run = storedRun()

	run, err := ocel.RetrieveRun(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("RetrieveRun() error = %v", err)
	}

	if req := only[*taskv1.RetrieveRunRequest](t, runtime); req.GetId() != "run-1" {
		t.Errorf("id = %q", req.GetId())
	}
	if run.ID != "run-1" || run.Task != "project-env-resize" || run.Status != ocel.RunTimedOut ||
		string(run.Payload) != `{"key":"a.png"}` || string(run.Output) != "null" ||
		run.Error != "the attempt ran past its max duration" || run.Attempts != 1 ||
		!slices.Equal(run.Tags, []string{"user:1"}) || run.Metadata["source"] != "upload" {
		t.Errorf("run = %+v", run)
	}
	if !run.CreatedAt.Equal(createdAt) || !run.DueAt.Equal(createdAt.Add(time.Minute)) ||
		!run.StartedAt.Equal(createdAt.Add(2*time.Minute)) || !run.FinishedAt.Equal(createdAt.Add(3*time.Minute)) ||
		!run.ExpiresAt.Equal(createdAt.Add(time.Hour)) {
		t.Errorf("run times = %+v", run)
	}
}

func TestListRunsNarrowsByTheBoundTaskStatusesAndTags(t *testing.T) {
	runtime := serveRuntime(t, boundTask("resize-listed"))
	runtime.run = storedRun()

	page, err := ocel.ListRuns(t.Context(),
		ocel.ForTask("resize-listed"),
		ocel.Statuses(ocel.RunFailed, ocel.RunTimedOut),
		ocel.Tags("user:1"),
		ocel.Cursor("c-1"),
		ocel.Limit(50),
	)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}

	req := only[*taskv1.ListRunsRequest](t, runtime)
	if req.GetTask() != "project-env-resize" || req.GetCursor() != "c-1" || req.GetLimit() != 50 ||
		!slices.Equal(req.GetTags(), []string{"user:1"}) ||
		!slices.Equal(req.GetStatuses(), []taskv1.RunStatus{taskv1.RunStatus_RUN_STATUS_FAILED, taskv1.RunStatus_RUN_STATUS_TIMED_OUT}) {
		t.Errorf("request = %v", req)
	}
	if page.NextCursor != "after-runs" || len(page.Runs) != 1 || page.Runs[0].ID != "run-1" {
		t.Errorf("page = %+v", page)
	}
}

func TestCancelRunCancelsTheRunAndReturnsItsRecord(t *testing.T) {
	runtime := serveRuntime(t, nil)
	runtime.run = storedRun()

	run, err := ocel.CancelRun(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("CancelRun() error = %v", err)
	}

	if req := only[*taskv1.CancelRunRequest](t, runtime); req.GetId() != "run-1" || run.ID != "run-1" {
		t.Errorf("CancelRun() = %+v after %v", run, req)
	}
}

func TestReplayRunReturnsTheNewRunsHandle(t *testing.T) {
	runtime := serveRuntime(t, nil)

	run, err := ocel.ReplayRun(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("ReplayRun() error = %v", err)
	}

	if req := only[*taskv1.ReplayRunRequest](t, runtime); req.GetId() != "run-1" || run != (ocel.RunHandle{ID: "run-2"}) {
		t.Errorf("ReplayRun() = %+v after %v", run, req)
	}
}

func TestRescheduleRunMovesTheRunDueByADelayFromNow(t *testing.T) {
	runtime := serveRuntime(t, nil)
	runtime.run = storedRun()

	before := time.Now()
	if _, err := ocel.RescheduleRun(t.Context(), "run-1", ocel.Delay(10*time.Minute)); err != nil {
		t.Fatalf("RescheduleRun() error = %v", err)
	}

	req := only[*taskv1.RescheduleRunRequest](t, runtime)
	due := req.GetDueAt().AsTime()
	if req.GetId() != "run-1" || due.Before(before.Add(10*time.Minute)) || due.After(time.Now().Add(10*time.Minute)) {
		t.Errorf("request = %v, want the run due ten minutes from now", req)
	}
}

func TestRunOperationsRefuseDuringDiscovery(t *testing.T) {
	discoveryDeclarations(t)

	for access, err := range map[string]error{
		"RetrieveRun":   second(ocel.RetrieveRun(t.Context(), "run-1")),
		"ListRuns":      second(ocel.ListRuns(t.Context())),
		"CancelRun":     second(ocel.CancelRun(t.Context(), "run-1")),
		"ReplayRun":     second(ocel.ReplayRun(t.Context(), "run-1")),
		"RescheduleRun": second(ocel.RescheduleRun(t.Context(), "run-1", ocel.Delay(time.Minute))),
	} {
		var unprovisioned *ocel.UnprovisionedError
		if !errors.As(err, &unprovisioned) || unprovisioned.Resource != "runs" || unprovisioned.Access != access {
			t.Errorf("%s error = %v, want an *UnprovisionedError naming it", access, err)
		}
	}
}

func TestListRunsRefusesAStatusItDoesNotKnow(t *testing.T) {
	runtime := serveRuntime(t, nil)

	_, err := ocel.ListRuns(t.Context(), ocel.Statuses(ocel.RunFailed, "RUNNING"))

	if err == nil || !strings.Contains(err.Error(), `"RUNNING"`) || len(runtime.requests()) != 0 {
		t.Errorf("ListRuns() error = %v after %d requests, want an error naming the status and no request", err, len(runtime.requests()))
	}
}
