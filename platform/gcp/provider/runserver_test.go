package gcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	run "google.golang.org/api/run/v2"
)

const trafficByLatest = "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST"

type runServer struct {
	mu       sync.Mutex
	held     *run.GoogleCloudRunV2Service
	revision int
	writes   int

	created  []*run.GoogleCloudRunV2Service
	patched  []*run.GoogleCloudRunV2Service
	policies []*run.GoogleIamV1Policy

	patchConflicts int
	patchTries     int
}

func (s *runServer) open(t *testing.T) *Provider {
	t.Helper()
	server := httptest.NewServer(s.serve(t))
	t.Cleanup(server.Close)
	return pushing(t, server.URL)
}

func (s *runServer) serve(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(path, ":setIamPolicy"):
			s.setPolicy(w, r)
		case r.Method == http.MethodGet && strings.HasSuffix(path, ":getIamPolicy"):
			writeBody(w, &run.GoogleIamV1Policy{Etag: "BwXhoLA="})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/services"):
			s.create(w, r)
		case r.Method == http.MethodPatch:
			s.patch(w, r)
		case r.Method == http.MethodGet && strings.Contains(path, "/operations/"):
			writeBody(w, &run.GoogleLongrunningOperation{Name: strings.TrimPrefix(path, "/v2/"), Done: true})
		case r.Method == http.MethodGet:
			s.get(w)
		default:
			t.Errorf("the release called %s %s, which nothing here serves", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func (s *runServer) create(w http.ResponseWriter, r *http.Request) {
	desired := readBody[run.GoogleCloudRunV2Service](w, r)
	if desired == nil {
		return
	}
	s.created = append(s.created, desired)
	name := r.URL.Query().Get("serviceId")
	s.held = &run.GoogleCloudRunV2Service{
		Name:     strings.TrimPrefix(r.URL.Path, "/v2/") + "/" + name,
		Template: desired.Template,
		Traffic:  allocated(desired.Traffic),
		Uri:      "https://" + name + ".run.app",
	}
	s.revised()
	writeBody(w, &run.GoogleLongrunningOperation{Name: "operations/stand", Done: true})
}

func (s *runServer) patch(w http.ResponseWriter, r *http.Request) {
	desired := readBody[run.GoogleCloudRunV2Service](w, r)
	if desired == nil {
		return
	}
	s.patchTries++
	if s.patchConflicts > 0 {
		s.patchConflicts--
		conflicted(w, "the service was changed under this release")
		return
	}
	s.patched = append(s.patched, desired)
	replaced := !sameTemplate(s.held.Template, desired.Template)
	s.held.Template = desired.Template
	s.held.Traffic = allocated(desired.Traffic)
	s.writes++
	s.held.Etag = "etag-" + strconv.Itoa(s.writes)
	if replaced {
		s.revised()
	}
	writeBody(w, &run.GoogleLongrunningOperation{Name: "operations/release", Done: true})
}

func (s *runServer) revised() {
	s.revision++
	s.writes++
	s.held.LatestReadyRevision = s.held.Name + "/revisions/" + revisionName(s.held.Name) + "-0000" + strconv.Itoa(s.revision)
	s.held.Etag = "etag-" + strconv.Itoa(s.writes)
}

func allocated(traffic []*run.GoogleCloudRunV2TrafficTarget) []*run.GoogleCloudRunV2TrafficTarget {
	if len(traffic) > 0 {
		return traffic
	}
	return []*run.GoogleCloudRunV2TrafficTarget{{Type: trafficByLatest, Percent: 100}}
}

func sameTemplate(held, desired *run.GoogleCloudRunV2RevisionTemplate) bool {
	was, _ := json.Marshal(held)
	is, _ := json.Marshal(desired)
	return string(was) == string(is)
}

func (s *runServer) get(w http.ResponseWriter) {
	if s.held == nil {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":404,"message":"service not found"}}`))
		return
	}
	writeBody(w, s.held)
}

func (s *runServer) setPolicy(w http.ResponseWriter, r *http.Request) {
	asked := readBody[run.GoogleIamV1SetIamPolicyRequest](w, r)
	if asked == nil {
		return
	}
	s.policies = append(s.policies, asked.Policy)
	writeBody(w, asked.Policy)
}

func conflicted(w http.ResponseWriter, said string) {
	w.WriteHeader(http.StatusConflict)
	w.Write([]byte(`{"error":{"code":409,"message":"` + said + `"}}`))
}

func readBody[T any](w http.ResponseWriter, r *http.Request) *T {
	var body T
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return nil
	}
	return &body
}

func writeBody(w http.ResponseWriter, body any) {
	if err := json.NewEncoder(w).Encode(body); err != nil {
		panic(err)
	}
}

func (s *runServer) bound() []*run.GoogleIamV1Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.policies)
}

func (s *runServer) standing() *run.GoogleCloudRunV2Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.created) == 0 {
		return &run.GoogleCloudRunV2Service{}
	}
	return s.created[len(s.created)-1]
}

func (s *runServer) serving() *run.GoogleCloudRunV2Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held
}

func (s *runServer) releases() []*run.GoogleCloudRunV2Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.patched)
}

func (s *runServer) tries() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.patchTries
}
