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
	mu        sync.Mutex
	service   *run.GoogleCloudRunV2Service
	revision  int
	revisions []string
	writes    int

	created  []*run.GoogleCloudRunV2Service
	patched  []*run.GoogleCloudRunV2Service
	policies []*run.GoogleIamV1Policy

	patchConflicts int
	patchTries     int

	uriEachRevision bool
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
		case r.Method == http.MethodDelete && strings.Contains(path, "/revisions/"):
			s.deleteRevision(w, revisionName(path))
		case r.Method == http.MethodDelete:
			s.service = nil
			s.revisions = nil
			writeBody(w, &run.GoogleLongrunningOperation{Name: "operations/delete", Done: true})
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
	s.service = &run.GoogleCloudRunV2Service{
		Name:               strings.TrimPrefix(r.URL.Path, "/v2/") + "/" + name,
		Template:           desired.Template,
		Traffic:            allocated(desired.Traffic),
		Ingress:            desired.Ingress,
		InvokerIamDisabled: desired.InvokerIamDisabled,
		Uri:                "https://" + name + ".run.app",
	}
	s.revised()
	writeBody(w, &run.GoogleLongrunningOperation{Name: "operations/create", Done: true})
}

func (s *runServer) patch(w http.ResponseWriter, r *http.Request) {
	desired := readBody[run.GoogleCloudRunV2Service](w, r)
	if desired == nil {
		return
	}
	s.patchTries++
	if desired.Etag != "" && desired.Etag != s.service.Etag {
		conflicted(w, "the service's etag is "+s.service.Etag+", not the "+desired.Etag+" this write was based on")
		return
	}
	if s.patchConflicts > 0 {
		s.patchConflicts--
		conflicted(w, "the service was changed under this release")
		return
	}
	s.patched = append(s.patched, desired)
	if mask := r.URL.Query().Get("updateMask"); mask != "" {
		if slices.Contains(strings.Split(mask, ","), "traffic") {
			s.service.Traffic = allocated(desired.Traffic)
		}
		s.writes++
		s.service.Etag = "etag-" + strconv.Itoa(s.writes)
		writeBody(w, &run.GoogleLongrunningOperation{Name: "operations/release", Done: true})
		return
	}
	replaced := !sameTemplate(s.service.Template, desired.Template)
	s.service.Template = desired.Template
	s.service.Traffic = allocated(desired.Traffic)
	s.service.Ingress = desired.Ingress
	s.service.InvokerIamDisabled = desired.InvokerIamDisabled
	s.writes++
	s.service.Etag = "etag-" + strconv.Itoa(s.writes)
	if replaced {
		s.revised()
	}
	writeBody(w, &run.GoogleLongrunningOperation{Name: "operations/release", Done: true})
}

func (s *runServer) revised() {
	s.revision++
	s.writes++
	s.service.LatestReadyRevision = s.service.Name + "/revisions/" + revisionName(s.service.Name) + "-0000" + strconv.Itoa(s.revision)
	if s.uriEachRevision {
		s.service.Uri = "https://" + revisionName(s.service.Name) + "-" + strconv.Itoa(s.revision) + ".run.app"
	}
	s.service.Etag = "etag-" + strconv.Itoa(s.writes)
	s.revisions = append(s.revisions, revisionName(s.service.LatestReadyRevision))
}

func (s *runServer) deleteRevision(w http.ResponseWriter, revision string) {
	if s.service == nil || !slices.Contains(s.revisions, revision) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":404,"message":"revision not found"}}`))
		return
	}
	routed := slices.ContainsFunc(s.service.Traffic, func(target *run.GoogleCloudRunV2TrafficTarget) bool {
		return revisionName(target.Revision) == revision ||
			target.Type == trafficByLatest && revisionName(s.service.LatestReadyRevision) == revision
	})
	if routed || revisionName(s.service.LatestReadyRevision) == revision {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"revision ` + revision + ` is the latest or can receive traffic"}}`))
		return
	}
	s.revisions = slices.DeleteFunc(s.revisions, func(standing string) bool { return standing == revision })
	writeBody(w, &run.GoogleLongrunningOperation{Name: "operations/delete-revision", Done: true})
}

func allocated(traffic []*run.GoogleCloudRunV2TrafficTarget) []*run.GoogleCloudRunV2TrafficTarget {
	if len(traffic) > 0 {
		return traffic
	}
	return []*run.GoogleCloudRunV2TrafficTarget{{Type: trafficByLatest, Percent: 100}}
}

func sameTemplate(current, desired *run.GoogleCloudRunV2RevisionTemplate) bool {
	was, _ := json.Marshal(current)
	is, _ := json.Marshal(desired)
	return string(was) == string(is)
}

func (s *runServer) get(w http.ResponseWriter) {
	if s.service == nil {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":404,"message":"service not found"}}`))
		return
	}
	writeBody(w, s.service)
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

func (s *runServer) current() *run.GoogleCloudRunV2Service {
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
	return s.service
}

func (s *runServer) releases() []*run.GoogleCloudRunV2Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.patched)
}

func (s *runServer) standing() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.revisions)
}

func (s *runServer) tries() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.patchTries
}
