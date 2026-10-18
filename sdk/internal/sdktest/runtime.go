package sdktest

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

const SessionToken = "runtime-token"

const ExactJSON = `{"ratio":2.0,"id":9007199254740993,"count":2}`

type Runtime struct {
	mu       sync.Mutex
	received []proto.Message
	Run      *taskv1.Run
	Letters  []*topicv1.DeadLetter
	Refusal  error
}

func (f *Runtime) record(req proto.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.received = append(f.received, req)
}

func (f *Runtime) Requests() []proto.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]proto.Message(nil), f.received...)
}

func (f *Runtime) Send(_ context.Context, req *topicv1.SendRequest) (*topicv1.SendResponse, error) {
	f.record(req)
	if f.Refusal != nil {
		return nil, f.Refusal
	}
	return &topicv1.SendResponse{MessageId: "01HZY4B6Q0Z0Z0Z0Z0Z0Z0Z0Z0"}, nil
}

func (f *Runtime) ListDeadLetters(_ context.Context, req *topicv1.ListDeadLettersRequest) (*topicv1.ListDeadLettersResponse, error) {
	f.record(req)
	return &topicv1.ListDeadLettersResponse{DeadLetters: f.Letters, NextCursor: "after-letters"}, nil
}

func (f *Runtime) RedriveDeadLetters(_ context.Context, req *topicv1.RedriveDeadLettersRequest) (*topicv1.RedriveDeadLettersResponse, error) {
	f.record(req)
	return &topicv1.RedriveDeadLettersResponse{Redriven: 3}, nil
}

func (f *Runtime) PurgeDeadLetters(_ context.Context, req *topicv1.PurgeDeadLettersRequest) (*topicv1.PurgeDeadLettersResponse, error) {
	f.record(req)
	return &topicv1.PurgeDeadLettersResponse{Purged: 2}, nil
}

func (f *Runtime) CountDeadLetters(_ context.Context, req *topicv1.CountDeadLettersRequest) (*topicv1.CountDeadLettersResponse, error) {
	f.record(req)
	return &topicv1.CountDeadLettersResponse{Count: 7}, nil
}

func (f *Runtime) Trigger(_ context.Context, req *taskv1.TriggerRequest) (*taskv1.TriggerResponse, error) {
	f.record(req)
	if f.Refusal != nil {
		return nil, f.Refusal
	}
	return &taskv1.TriggerResponse{Id: "run-1"}, nil
}

func (f *Runtime) BatchTrigger(_ context.Context, req *taskv1.BatchTriggerRequest) (*taskv1.BatchTriggerResponse, error) {
	f.record(req)
	res := &taskv1.BatchTriggerResponse{}
	for i := range req.GetItems() {
		res.Ids = append(res.Ids, fmt.Sprintf("run-%d", i+1))
	}
	return res, nil
}

func (f *Runtime) RetrieveRun(_ context.Context, req *taskv1.RetrieveRunRequest) (*taskv1.RetrieveRunResponse, error) {
	f.record(req)
	return &taskv1.RetrieveRunResponse{Run: f.Run}, nil
}

func (f *Runtime) ListRuns(_ context.Context, req *taskv1.ListRunsRequest) (*taskv1.ListRunsResponse, error) {
	f.record(req)
	return &taskv1.ListRunsResponse{Runs: []*taskv1.Run{f.Run}, NextCursor: "after-runs"}, nil
}

func (f *Runtime) CancelRun(_ context.Context, req *taskv1.CancelRunRequest) (*taskv1.CancelRunResponse, error) {
	f.record(req)
	return &taskv1.CancelRunResponse{Run: f.Run}, nil
}

func (f *Runtime) ReplayRun(_ context.Context, req *taskv1.ReplayRunRequest) (*taskv1.ReplayRunResponse, error) {
	f.record(req)
	return &taskv1.ReplayRunResponse{Id: "run-2"}, nil
}

func (f *Runtime) RescheduleRun(_ context.Context, req *taskv1.RescheduleRunRequest) (*taskv1.RescheduleRunResponse, error) {
	f.record(req)
	return &taskv1.RescheduleRunResponse{Run: f.Run}, nil
}

func ServeRuntime(t *testing.T, bindings map[string]string) *Runtime {
	t.Helper()
	runtime := &Runtime{}
	mux := http.NewServeMux()
	topicPath, topicHandler := topicv1connect.NewTopicServiceHandler(runtime)
	taskPath, taskHandler := taskv1connect.NewTaskServiceHandler(runtime)
	for path, handler := range map[string]http.Handler{topicPath: topicHandler, taskPath: taskHandler} {
		mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+SessionToken {
				http.Error(w, "this request has no valid session token", http.StatusForbidden)
				return
			}
			handler.ServeHTTP(w, r)
		}))
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("OCEL_RUNTIME_ADDRESS", srv.URL)
	t.Setenv("OCEL_SESSION_TOKEN", SessionToken)
	for key, value := range bindings {
		t.Setenv(key, value)
	}
	return runtime
}

func TaskBinding(name string) map[string]string {
	return map[string]string{"OCEL_RESOURCE_TASK_" + name: fmt.Sprintf(`{"name":%q,"task":{}}`, name)}
}

func TopicBinding(name string) map[string]string {
	return map[string]string{"OCEL_RESOURCE_TOPIC_" + name: fmt.Sprintf(`{"name":%q,"topic":{}}`, name)}
}

func Only[M proto.Message](t *testing.T, runtime *Runtime) M {
	t.Helper()
	received := runtime.Requests()
	if len(received) != 1 {
		t.Fatalf("the runtime received %v, want one request", Names(received))
	}
	req, ok := received[0].(M)
	if !ok {
		t.Fatalf("the runtime received %v", Names(received))
	}
	return req
}

func Names(received []proto.Message) []string {
	var out []string
	for _, req := range received {
		out = append(out, string(req.ProtoReflect().Descriptor().FullName()))
	}
	return out
}
