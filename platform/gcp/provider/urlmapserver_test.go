package gcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/compute/v1"
)

type urlMapServer struct {
	mu        sync.Mutex
	held      *compute.UrlMap
	conflicts int
	tries     int
}

func routing(t *testing.T, held *compute.UrlMap) (*Provider, *urlMapServer) {
	t.Helper()
	server := &urlMapServer{held: held}
	served := httptest.NewServer(server.serve(t))
	t.Cleanup(served.Close)
	return pushing(t, served.URL), server
}

func (s *urlMapServer) serve(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/global/operations/"):
			writeBody(w, &compute.Operation{Name: "op", Status: operationDone})
		case r.Method == http.MethodGet:
			writeBody(w, s.held)
		case r.Method == http.MethodPatch:
			s.patch(w, r)
		default:
			t.Errorf("the route called %s %s, which nothing here serves", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func (s *urlMapServer) patch(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var said map[string]json.RawMessage
	var desired compute.UrlMap
	if err := json.Unmarshal(body, &said); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal(body, &desired); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.tries++
	if s.conflicts > 0 {
		s.conflicts--
		s.held.Fingerprint = strconv.Itoa(s.tries)
		conflicted(w, "the url map changed under this write")
		return
	}
	if desired.Fingerprint != s.held.Fingerprint {
		conflicted(w, "the url map fingerprint is stale")
		return
	}
	if _, carried := said["hostRules"]; carried {
		s.held.HostRules = desired.HostRules
	}
	if _, carried := said["pathMatchers"]; carried {
		s.held.PathMatchers = desired.PathMatchers
	}
	s.held.Fingerprint = strconv.Itoa(s.tries)
	writeBody(w, &compute.Operation{Name: "op", Status: operationDone})
}

func (s *urlMapServer) standing() *compute.UrlMap {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held
}

func (s *urlMapServer) writes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tries
}
