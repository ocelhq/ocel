package alb

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"google.golang.org/api/compute/v1"
)

type urlMapServer struct {
	mu        sync.Mutex
	urlMap    *compute.UrlMap
	conflicts int
	tries     int
	fullURLs  bool
}

func mapping(endpoint string) *URLMaps {
	open := googletest.Opener(googletest.Clients(endpoint))
	return &URLMaps{OpenClients: open, Services: &run.Services{OpenClients: open}}
}

func routing(t *testing.T, initial *compute.UrlMap) (*URLMaps, *urlMapServer) {
	t.Helper()
	server := &urlMapServer{urlMap: initial}
	served := httptest.NewServer(server.serve(t))
	t.Cleanup(served.Close)
	return mapping(served.URL), server
}

func (s *urlMapServer) serve(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/global/operations/"):
			googletest.WriteBody(w, &compute.Operation{Name: "op", Status: gcloud.OperationDone})
		case r.Method == http.MethodGet && s.fullURLs:
			googletest.WriteBody(w, withFullURLs(t, s.urlMap))
		case r.Method == http.MethodGet:
			googletest.WriteBody(w, s.urlMap)
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
		s.urlMap.Fingerprint = strconv.Itoa(s.tries)
		googletest.Conflicted(w, "the url map changed under this write")
		return
	}
	if desired.Fingerprint != s.urlMap.Fingerprint {
		googletest.Conflicted(w, "the url map fingerprint is stale")
		return
	}
	if _, sent := said["hostRules"]; sent {
		s.urlMap.HostRules = desired.HostRules
	}
	if _, sent := said["pathMatchers"]; sent {
		s.urlMap.PathMatchers = desired.PathMatchers
	}
	s.urlMap.Fingerprint = strconv.Itoa(s.tries)
	googletest.WriteBody(w, &compute.Operation{Name: "op", Status: gcloud.OperationDone})
}

func (s *urlMapServer) current() *compute.UrlMap {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.urlMap
}

func (s *urlMapServer) writes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tries
}

func (s *urlMapServer) answersFullURLs() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fullURLs = true
}

const computeURL = "https://www.googleapis.com/compute/v1/"

func withFullURLs(t *testing.T, urlMap *compute.UrlMap) *compute.UrlMap {
	t.Helper()
	encoded, err := json.Marshal(urlMap)
	if err != nil {
		t.Fatalf("encode the url map: %v", err)
	}
	var full compute.UrlMap
	if err := json.Unmarshal(encoded, &full); err != nil {
		t.Fatalf("decode the url map: %v", err)
	}
	expand := func(link string) string {
		if link == "" || strings.HasPrefix(link, "https://") {
			return link
		}
		return computeURL + link
	}
	full.DefaultService = expand(full.DefaultService)
	for _, matcher := range full.PathMatchers {
		matcher.DefaultService = expand(matcher.DefaultService)
		for _, rule := range matcher.RouteRules {
			rule.Service = expand(rule.Service)
		}
	}
	return &full
}
