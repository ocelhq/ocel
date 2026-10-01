package ocel_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"
	taskv1 "ocel.dev/internal/proto/app/task/v1"
	"ocel.dev/internal/proto/app/task/v1/taskv1connect"
	topicv1 "ocel.dev/internal/proto/app/topic/v1"
	"ocel.dev/internal/proto/app/topic/v1/topicv1connect"
)

type fakeRuntime struct {
	mu       sync.Mutex
	received []proto.Message
	run      *taskv1.Run
	letters  []*topicv1.DeadLetter
}

func (f *fakeRuntime) record(req proto.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.received = append(f.received, req)
}

func (f *fakeRuntime) requests() []proto.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]proto.Message(nil), f.received...)
}

func (f *fakeRuntime) Send(_ context.Context, req *topicv1.SendRequest) (*topicv1.SendResponse, error) {
	f.record(req)
	return &topicv1.SendResponse{MessageId: "01HZY4B6Q0Z0Z0Z0Z0Z0Z0Z0Z0"}, nil
}

func (f *fakeRuntime) ListDeadLetters(_ context.Context, req *topicv1.ListDeadLettersRequest) (*topicv1.ListDeadLettersResponse, error) {
	f.record(req)
	return &topicv1.ListDeadLettersResponse{DeadLetters: f.letters, NextCursor: "after-letters"}, nil
}

func (f *fakeRuntime) RedriveDeadLetters(_ context.Context, req *topicv1.RedriveDeadLettersRequest) (*topicv1.RedriveDeadLettersResponse, error) {
	f.record(req)
	return &topicv1.RedriveDeadLettersResponse{Redriven: 3}, nil
}

func (f *fakeRuntime) PurgeDeadLetters(_ context.Context, req *topicv1.PurgeDeadLettersRequest) (*topicv1.PurgeDeadLettersResponse, error) {
	f.record(req)
	return &topicv1.PurgeDeadLettersResponse{Purged: 2}, nil
}

func (f *fakeRuntime) CountDeadLetters(_ context.Context, req *topicv1.CountDeadLettersRequest) (*topicv1.CountDeadLettersResponse, error) {
	f.record(req)
	return &topicv1.CountDeadLettersResponse{Count: 7}, nil
}

func (f *fakeRuntime) Trigger(_ context.Context, req *taskv1.TriggerRequest) (*taskv1.TriggerResponse, error) {
	f.record(req)
	return &taskv1.TriggerResponse{Id: "run-1"}, nil
}

func (f *fakeRuntime) BatchTrigger(_ context.Context, req *taskv1.BatchTriggerRequest) (*taskv1.BatchTriggerResponse, error) {
	f.record(req)
	res := &taskv1.BatchTriggerResponse{}
	for i := range req.GetItems() {
		res.Ids = append(res.Ids, fmt.Sprintf("run-%d", i+1))
	}
	return res, nil
}

func (f *fakeRuntime) RetrieveRun(_ context.Context, req *taskv1.RetrieveRunRequest) (*taskv1.RetrieveRunResponse, error) {
	f.record(req)
	return &taskv1.RetrieveRunResponse{Run: f.run}, nil
}

func (f *fakeRuntime) ListRuns(_ context.Context, req *taskv1.ListRunsRequest) (*taskv1.ListRunsResponse, error) {
	f.record(req)
	return &taskv1.ListRunsResponse{Runs: []*taskv1.Run{f.run}, NextCursor: "after-runs"}, nil
}

func (f *fakeRuntime) CancelRun(_ context.Context, req *taskv1.CancelRunRequest) (*taskv1.CancelRunResponse, error) {
	f.record(req)
	return &taskv1.CancelRunResponse{Run: f.run}, nil
}

func (f *fakeRuntime) ReplayRun(_ context.Context, req *taskv1.ReplayRunRequest) (*taskv1.ReplayRunResponse, error) {
	f.record(req)
	return &taskv1.ReplayRunResponse{Id: "run-2"}, nil
}

func (f *fakeRuntime) RescheduleRun(_ context.Context, req *taskv1.RescheduleRunRequest) (*taskv1.RescheduleRunResponse, error) {
	f.record(req)
	return &taskv1.RescheduleRunResponse{Run: f.run}, nil
}

func serveRuntime(t *testing.T, bindings map[string]string) *fakeRuntime {
	t.Helper()
	runtime := &fakeRuntime{}
	mux := http.NewServeMux()
	topicPath, topicHandler := topicv1connect.NewTopicServiceHandler(runtime)
	taskPath, taskHandler := taskv1connect.NewTaskServiceHandler(runtime)
	for path, handler := range map[string]http.Handler{topicPath: topicHandler, taskPath: taskHandler} {
		mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+storeToken {
				http.Error(w, "this request has no valid session token", http.StatusForbidden)
				return
			}
			handler.ServeHTTP(w, r)
		}))
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("OCEL_RUNTIME_ADDRESS", srv.URL)
	t.Setenv("OCEL_SESSION_TOKEN", storeToken)
	for key, value := range bindings {
		t.Setenv(key, value)
	}
	return runtime
}

func only[M proto.Message](t *testing.T, runtime *fakeRuntime) M {
	t.Helper()
	received := runtime.requests()
	if len(received) != 1 {
		t.Fatalf("the runtime received %v, want one request", names(received))
	}
	req, ok := received[0].(M)
	if !ok {
		t.Fatalf("the runtime received %v", names(received))
	}
	return req
}

func names(received []proto.Message) []string {
	var out []string
	for _, req := range received {
		out = append(out, string(req.ProtoReflect().Descriptor().FullName()))
	}
	return out
}
