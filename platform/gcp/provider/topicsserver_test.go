package gcp

import (
	"cmp"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	cloudscheduler "google.golang.org/api/cloudscheduler/v1"
)

type topicsServer struct {
	mu            sync.Mutex
	topics        map[string]map[string]any
	subscriptions map[string]map[string]any
	jobs          map[string]*cloudscheduler.Job
	writes        []string
	created       map[string]map[string]any
	patches       []sentPatch
}

type sentPatch struct {
	name string
	mask []string
	body map[string]any
}

func servingTopics(t *testing.T) (*Provider, *topicsServer) {
	t.Helper()
	server := &topicsServer{
		topics: map[string]map[string]any{}, subscriptions: map[string]map[string]any{},
		jobs: map[string]*cloudscheduler.Job{}, created: map[string]map[string]any{},
	}
	served := httptest.NewServer(http.HandlerFunc(server.serve))
	t.Cleanup(served.Close)
	return pushing(t, served.URL), server
}

func (s *topicsServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		s.writes = append(s.writes, r.Method+" "+r.URL.Path)
	}
	name := strings.TrimPrefix(r.URL.Path, "/v1/")
	switch {
	case strings.Contains(name, "/topics/"):
		s.serveHeld(w, r, s.topics, name, "topic")
	case strings.Contains(name, "/subscriptions/"):
		s.serveHeld(w, r, s.subscriptions, name, "subscription")
	case strings.HasSuffix(name, "/jobs") && r.Method == http.MethodPost:
		body, _ := io.ReadAll(r.Body)
		var job cloudscheduler.Job
		_ = json.Unmarshal(body, &job)
		if s.jobs[job.Name] != nil {
			googletest.Conflicted(w, "the job exists")
			return
		}
		withRetryDefaults(&job)
		s.jobs[job.Name] = &job
		googletest.WriteBody(w, &job)
	case strings.Contains(name, "/jobs/"):
		s.serveJob(w, r, name)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (s *topicsServer) serveHeld(w http.ResponseWriter, r *http.Request, held map[string]map[string]any, name, kind string) {
	switch r.Method {
	case http.MethodPut:
		if held[name] != nil {
			googletest.Conflicted(w, "Resource already exists in the project")
			return
		}
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["name"] = name
		held[name] = body
		s.created[name] = maps.Clone(body)
		googletest.WriteBody(w, body)
	case http.MethodGet, http.MethodDelete, http.MethodPatch:
		current := held[name]
		if current == nil {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":404,"message":"Resource not found"}}`))
			return
		}
		switch r.Method {
		case http.MethodDelete:
			delete(held, name)
		case http.MethodPatch:
			var asked map[string]any
			_ = json.NewDecoder(r.Body).Decode(&asked)
			body, _ := asked[kind].(map[string]any)
			mask, _ := asked["updateMask"].(string)
			s.applyMask(current, name, mask, body)
		}
		googletest.WriteBody(w, current)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (s *topicsServer) applyMask(current map[string]any, name, mask string, body map[string]any) {
	fields := strings.Split(mask, ",")
	for _, field := range fields {
		if value, sent := body[field]; sent {
			current[field] = value
			continue
		}
		delete(current, field)
	}
	s.patches = append(s.patches, sentPatch{name: name, mask: fields, body: body})
}

func (s *topicsServer) serveJob(w http.ResponseWriter, r *http.Request, name string) {
	current := s.jobs[name]
	if current == nil {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":404,"message":"Job not found"}}`))
		return
	}
	switch r.Method {
	case http.MethodGet:
		googletest.WriteBody(w, current)
	case http.MethodPatch:
		var held, body map[string]any
		encoded, _ := json.Marshal(current)
		_ = json.Unmarshal(encoded, &held)
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.applyMask(held, name, r.URL.Query().Get("updateMask"), body)
		held["name"] = name
		encoded, _ = json.Marshal(held)
		var job cloudscheduler.Job
		_ = json.Unmarshal(encoded, &job)
		withRetryDefaults(&job)
		s.jobs[name] = &job
		googletest.WriteBody(w, &job)
	case http.MethodDelete:
		delete(s.jobs, name)
		googletest.WriteBody(w, map[string]string{})
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func withRetryDefaults(job *cloudscheduler.Job) {
	if job.RetryConfig == nil {
		job.RetryConfig = &cloudscheduler.RetryConfig{}
	}
	retry := job.RetryConfig
	retry.MaxRetryDuration = cmp.Or(retry.MaxRetryDuration, "0s")
	retry.MinBackoffDuration = cmp.Or(retry.MinBackoffDuration, "5s")
	retry.MaxBackoffDuration = cmp.Or(retry.MaxBackoffDuration, "3600s")
	retry.MaxDoublings = cmp.Or(retry.MaxDoublings, 5)
}

func (s *topicsServer) wrote() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.writes)
}

func (s *topicsServer) sent() []sentPatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.patches)
}

func (s *topicsServer) holdSubscription(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscriptions[name] = map[string]any{"name": name, "messageRetentionDuration": "604800s"}
}

func (s *topicsServer) subscription(name string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.subscriptions[name])
}

func (s *topicsServer) topic(name string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.topics[name])
}

func (s *topicsServer) job(name string) *cloudscheduler.Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jobs[name]
}

func (s *topicsServer) forgetJob(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, name)
}
