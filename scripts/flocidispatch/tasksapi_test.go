package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const tasksPath = "/v2/projects/p/locations/r/queues/q/tasks"

type fakeCreate struct {
	lock     sync.Mutex
	requests []*cloudtaskspb.CreateTaskRequest
	err      error
	assigned string
}

func (f *fakeCreate) create(_ context.Context, request *cloudtaskspb.CreateTaskRequest) (*cloudtaskspb.Task, error) {
	f.lock.Lock()
	defer f.lock.Unlock()
	f.requests = append(f.requests, request)
	if f.err != nil {
		return nil, f.err
	}
	task := request.GetTask()
	if task.GetName() == "" && f.assigned != "" {
		task = &cloudtaskspb.Task{Name: f.assigned, MessageType: task.GetMessageType()}
	}
	return task, nil
}

func (f *fakeCreate) calls() int {
	f.lock.Lock()
	defer f.lock.Unlock()
	return len(f.requests)
}

func serveTasksAPI(t *testing.T, create *fakeCreate) (*tasksAPI, *httptest.Server) {
	t.Helper()
	key, err := newSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	api := newTasksAPI(create.create, key)
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	return api, server
}

func refreshTask(name string, body []byte) map[string]any {
	return map[string]any{"task": map[string]any{
		"name":             name,
		"dispatchDeadline": "60s",
		"httpRequest": map[string]any{
			"url":        "https://web-1.europe-west1.run.app/_ocel/refresh",
			"httpMethod": "POST",
			"headers": map[string]string{
				"Content-Type":             "application/json",
				"x-ocel-refresh-signature": "abc123",
			},
			"body": base64.StdEncoding.EncodeToString(body),
			"oidcToken": map[string]any{
				"serviceAccountEmail": refreshAccount,
				"audience":            refreshAudience,
			},
		},
	}}
}

func postTask(t *testing.T, server *httptest.Server, body any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(server.URL+tasksPath, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	said, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	_ = json.Unmarshal(said, &decoded)
	return resp.StatusCode, decoded
}

func errorStatusOf(answer map[string]any) string {
	failure, _ := answer["error"].(map[string]any)
	status, _ := failure["status"].(string)
	return status
}

func TestATaskPostedAsCloudTasksRESTIsCreatedInTheQueueItsPathNames(t *testing.T) {
	create := &fakeCreate{}
	_, server := serveTasksAPI(t, create)
	body := []byte("{\"isrPrefix\":\"p\",\"refresh\":{\"url\":\"/blog\"}}\n\x00")

	code, _ := postTask(t, server, refreshTask("projects/p/locations/r/queues/q/tasks/t1", body))

	if code != http.StatusOK {
		t.Fatalf("the answer is %d, want 200", code)
	}
	if create.calls() != 1 {
		t.Fatalf("floci was asked %d times, want once", create.calls())
	}
	request := create.requests[0]
	if request.GetParent() != "projects/p/locations/r/queues/q" {
		t.Errorf("Parent = %q, want the queue the path names", request.GetParent())
	}
	sent := request.GetTask().GetHttpRequest()
	if !bytes.Equal(sent.GetBody(), body) {
		t.Errorf("the body is %q, want the decoded bytes %q", sent.GetBody(), body)
	}
	if sent.GetHeaders()["x-ocel-refresh-signature"] != "abc123" || sent.GetHeaders()["Content-Type"] != "application/json" {
		t.Errorf("the headers are %v, want both the task's headers", sent.GetHeaders())
	}
	if sent.GetOidcToken().GetServiceAccountEmail() != refreshAccount || sent.GetOidcToken().GetAudience() != refreshAudience {
		t.Errorf("the oidcToken is %v, want the account and audience the task named", sent.GetOidcToken())
	}
}

func TestATaskNameCreatedOnceIsAnsweredAlreadyExistsWithoutAskingFlociAgain(t *testing.T) {
	create := &fakeCreate{}
	api, server := serveTasksAPI(t, create)
	task := refreshTask("projects/p/locations/r/queues/q/tasks/t1", []byte("{}"))
	postTask(t, server, task)

	code, answer := postTask(t, server, task)

	if code != http.StatusConflict || errorStatusOf(answer) != "ALREADY_EXISTS" {
		t.Errorf("the second create answered %d %v, want 409 ALREADY_EXISTS", code, answer)
	}
	if create.calls() != 1 {
		t.Errorf("floci was asked %d times, want once", create.calls())
	}
	if token, found := api.recordedToken("projects/p/locations/r/queues/q/tasks/t1"); !found || token.GetAudience() != refreshAudience {
		t.Errorf("recordedToken() = %v, %v; want the token the first create named", token, found)
	}
}

func TestATaskCreatedWithNoNameIsRecordedUnderTheNameFlociGaveIt(t *testing.T) {
	create := &fakeCreate{assigned: "projects/p/locations/r/queues/q/tasks/given"}
	api, server := serveTasksAPI(t, create)
	unnamed := refreshTask("", []byte("{}"))
	delete(unnamed["task"].(map[string]any), "name")

	code, _ := postTask(t, server, unnamed)

	if code != http.StatusOK {
		t.Fatalf("the answer is %d, want 200", code)
	}
	if token, found := api.recordedToken("projects/p/locations/r/queues/q/tasks/given"); !found || token.GetAudience() != refreshAudience {
		t.Errorf("recordedToken(given) = %v, %v; want the token the unnamed create named", token, found)
	}
}

func TestAnAbortedCreateIsAnsweredSoTheRuntimeRetriesIt(t *testing.T) {
	create := &fakeCreate{err: status.Error(codes.Aborted, "contention")}
	api, server := serveTasksAPI(t, create)
	task := refreshTask("projects/p/locations/r/queues/q/tasks/t1", []byte("{}"))

	code, answer := postTask(t, server, task)

	if code != http.StatusConflict || errorStatusOf(answer) != "ABORTED" {
		t.Errorf("the answer is %d %v, want 409 ABORTED", code, answer)
	}
	if _, found := api.recordedToken("projects/p/locations/r/queues/q/tasks/t1"); found {
		t.Error("a create floci aborted was recorded")
	}
	create.err = nil
	if code, _ := postTask(t, server, task); code != http.StatusOK {
		t.Errorf("the retry answered %d, want 200", code)
	}
	if create.calls() != 2 {
		t.Errorf("floci was asked %d times, want the retry to reach it", create.calls())
	}
}

func TestEachFlociRefusalIsAnsweredWithTheStatusCloudTasksRESTGives(t *testing.T) {
	tests := []struct {
		code   codes.Code
		http   int
		status string
	}{
		{codes.AlreadyExists, 409, "ALREADY_EXISTS"},
		{codes.Aborted, 409, "ABORTED"},
		{codes.InvalidArgument, 400, "INVALID_ARGUMENT"},
		{codes.FailedPrecondition, 400, "FAILED_PRECONDITION"},
		{codes.NotFound, 404, "NOT_FOUND"},
		{codes.ResourceExhausted, 429, "RESOURCE_EXHAUSTED"},
		{codes.Unavailable, 503, "UNAVAILABLE"},
		{codes.DeadlineExceeded, 504, "DEADLINE_EXCEEDED"},
		{codes.Internal, 500, "INTERNAL"},
		{codes.PermissionDenied, 500, "INTERNAL"},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			gotHTTP, gotStatus := mapRESTStatus(test.code)

			if gotHTTP != test.http || gotStatus != test.status {
				t.Errorf("mapRESTStatus(%v) = %d %s, want %d %s", test.code, gotHTTP, gotStatus, test.http, test.status)
			}
		})
	}
}

func TestTheCertsAnswerIsCachedForAnHour(t *testing.T) {
	_, server := serveTasksAPI(t, &fakeCreate{})

	resp, err := http.Get(server.URL + "/oauth2/v3/certs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "public, max-age=3600" ||
		resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("the answer is %d with Cache-Control %q and Content-Type %q",
			resp.StatusCode, resp.Header.Get("Cache-Control"), resp.Header.Get("Content-Type"))
	}
}
