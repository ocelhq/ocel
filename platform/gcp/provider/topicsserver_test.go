package gcp

import (
	"encoding/json"
	"io"
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
	topics        map[string]bool
	subscriptions map[string]bool
	jobs          map[string]*cloudscheduler.Job
	writes        []string
}

func servingTopics(t *testing.T) (*Provider, *topicsServer) {
	t.Helper()
	server := &topicsServer{topics: map[string]bool{}, subscriptions: map[string]bool{}, jobs: map[string]*cloudscheduler.Job{}}
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
		s.serveHeld(w, r, s.topics, name)
	case strings.Contains(name, "/subscriptions/"):
		s.serveHeld(w, r, s.subscriptions, name)
	case strings.HasSuffix(name, "/jobs") && r.Method == http.MethodPost:
		body, _ := io.ReadAll(r.Body)
		var job cloudscheduler.Job
		_ = json.Unmarshal(body, &job)
		if s.jobs[job.Name] != nil {
			googletest.Conflicted(w, "the job exists")
			return
		}
		s.jobs[job.Name] = &job
		googletest.WriteBody(w, &job)
	case strings.Contains(name, "/jobs/"):
		s.serveJob(w, r, name)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (s *topicsServer) serveHeld(w http.ResponseWriter, r *http.Request, held map[string]bool, name string) {
	switch r.Method {
	case http.MethodPut:
		if held[name] {
			googletest.Conflicted(w, "Resource already exists in the project")
			return
		}
		held[name] = true
		googletest.WriteBody(w, map[string]string{"name": name})
	case http.MethodGet, http.MethodDelete:
		if !held[name] {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":404,"message":"Resource not found"}}`))
			return
		}
		if r.Method == http.MethodDelete {
			delete(held, name)
		}
		googletest.WriteBody(w, map[string]string{"name": name})
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
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
		body, _ := io.ReadAll(r.Body)
		var job cloudscheduler.Job
		_ = json.Unmarshal(body, &job)
		job.Name = name
		s.jobs[name] = &job
		googletest.WriteBody(w, &job)
	case http.MethodDelete:
		delete(s.jobs, name)
		googletest.WriteBody(w, map[string]string{})
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (s *topicsServer) wrote() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.writes)
}

func (s *topicsServer) holdSubscription(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscriptions[name] = true
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
