package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	maxCreateBody = 1 << 20
	certsMaxAge   = "public, max-age=3600"
)

var errNoTagURL = errors.New("the service has no URL for that tag")

type createTask func(context.Context, *cloudtaskspb.CreateTaskRequest) (*cloudtaskspb.Task, error)

type tasksAPI struct {
	create createTask
	key    *signingKey
	now    func() time.Time
	mux    *http.ServeMux

	lock    sync.Mutex
	created map[string]*cloudtaskspb.OidcToken
}

func newTasksAPI(create createTask, key *signingKey) *tasksAPI {
	api := &tasksAPI{
		create:  create,
		key:     key,
		now:     time.Now,
		mux:     http.NewServeMux(),
		created: map[string]*cloudtaskspb.OidcToken{},
	}
	api.mux.HandleFunc("POST /v2/projects/{project}/locations/{location}/queues/{queue}/tasks", api.createTask)
	api.mux.HandleFunc("GET /oauth2/v3/certs", api.certs)
	return api
}

func (a *tasksAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mux.ServeHTTP(w, r)
}

func (a *tasksAPI) recordedToken(name string) (*cloudtaskspb.OidcToken, bool) {
	a.lock.Lock()
	defer a.lock.Unlock()
	token, found := a.created[name]
	return token, found
}

func (a *tasksAPI) createTask(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCreateBody))
	if err != nil {
		writeRefusal(w, http.StatusBadRequest, "INVALID_ARGUMENT", "the request body could not be read within 1 MiB")
		return
	}
	request := &cloudtaskspb.CreateTaskRequest{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(body, request); err != nil {
		writeRefusal(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	request.Parent = "projects/" + r.PathValue("project") + "/locations/" + r.PathValue("location") + "/queues/" + r.PathValue("queue")
	name := request.GetTask().GetName()
	if name != "" {
		if _, found := a.recordedToken(name); found {
			writeRefusal(w, http.StatusConflict, "ALREADY_EXISTS", "the task "+name+" was created through this API")
			return
		}
	}
	task, err := a.create(r.Context(), request)
	if err != nil {
		code, state := mapRESTStatus(status.Code(err))
		writeRefusal(w, code, state, status.Convert(err).Message())
		return
	}
	if created := task.GetName(); created != "" {
		a.lock.Lock()
		a.created[created] = request.GetTask().GetHttpRequest().GetOidcToken()
		a.lock.Unlock()
	}
	answer, err := protojson.Marshal(task)
	if err != nil {
		writeRefusal(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(answer)
}

func (a *tasksAPI) certs(w http.ResponseWriter, _ *http.Request) {
	keys, err := a.key.publicKeys()
	if err != nil {
		writeRefusal(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", certsMaxAge)
	_, _ = w.Write(keys)
}

func mapRESTStatus(code codes.Code) (int, string) {
	switch code {
	case codes.AlreadyExists:
		return http.StatusConflict, "ALREADY_EXISTS"
	case codes.Aborted:
		return http.StatusConflict, "ABORTED"
	case codes.InvalidArgument:
		return http.StatusBadRequest, "INVALID_ARGUMENT"
	case codes.FailedPrecondition:
		return http.StatusBadRequest, "FAILED_PRECONDITION"
	case codes.NotFound:
		return http.StatusNotFound, "NOT_FOUND"
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests, "RESOURCE_EXHAUSTED"
	case codes.Unavailable:
		return http.StatusServiceUnavailable, "UNAVAILABLE"
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout, "DEADLINE_EXCEEDED"
	}
	return http.StatusInternalServerError, "INTERNAL"
}

func writeRefusal(w http.ResponseWriter, code int, state, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": message, "status": state}})
}
