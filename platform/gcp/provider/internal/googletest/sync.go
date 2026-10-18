package googletest

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/cloudscheduler/v1"
	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

type Sync struct {
	*IAM

	mu        sync.Mutex
	Services  map[string]*runv2.GoogleCloudRunV2Service
	revisions map[string]int
	Policies  map[string]*runv2.GoogleIamV1Policy
	Schedules map[string]*cloudscheduler.Job
	Patched   []string
	deleted   []string
	Pushed    []string

	PolicyChanges int
}

func NewSync() *Sync {
	return &Sync{
		IAM:       GrantedIAM(),
		Services:  map[string]*runv2.GoogleCloudRunV2Service{},
		revisions: map[string]int{},
		Policies:  map[string]*runv2.GoogleIamV1Policy{},
		Schedules: map[string]*cloudscheduler.Job{},
	}
}

func ServedAt(name string) string { return "https://" + name + "-3k2x7lq4ta-ew.a.run.app" }

func (s *Sync) Open(t *testing.T) *gcloud.Clients {
	t.Helper()
	iamRest := s.Rest(t)
	c := s.Serve(t, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasPrefix(path, "/v2/") && strings.Contains(path, "/services"):
			s.serveRun(w, r, strings.TrimPrefix(path, "/v2/"))
		case strings.HasPrefix(path, "/v2/") && strings.Contains(path, "/operations/"):
			WriteBody(w, &runv2.GoogleLongrunningOperation{Name: strings.TrimPrefix(path, "/v2/"), Done: true})
		case strings.HasPrefix(path, "/v1/") && strings.Contains(path, "/jobs"):
			s.serveScheduler(w, r, strings.TrimPrefix(path, "/v1/"))
		default:
			iamRest(w, r)
		}
	})
	return c
}

func (s *Sync) Push(_ context.Context, _ environment.Tier, _, ref string, _ []byte, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Pushed = append(s.Pushed, ref)
	return nil
}

func notFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
}

func (s *Sync) serveRun(w http.ResponseWriter, r *http.Request, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	name, verb, _ := strings.Cut(path, ":")
	done := &runv2.GoogleLongrunningOperation{Name: "projects/acme-prod/locations/europe-west1/operations/op", Done: true}
	switch {
	case verb == "getIamPolicy":
		if policy := s.Policies[name]; policy != nil {
			WriteBody(w, policy)
			return
		}
		WriteBody(w, &runv2.GoogleIamV1Policy{Etag: "BwXhoLA="})
	case verb == "setIamPolicy":
		asked := ReadBody[runv2.GoogleIamV1SetIamPolicyRequest](w, r)
		if asked == nil {
			return
		}
		if s.PolicyChanges > 0 {
			s.PolicyChanges--
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = w.Write([]byte(`{"error":{"code":412,"message":"conditionNotMet"}}`))
			return
		}
		s.Policies[name] = asked.Policy
		WriteBody(w, asked.Policy)
	case r.Method == http.MethodPost:
		desired := ReadBody[runv2.GoogleCloudRunV2Service](w, r)
		if desired == nil {
			return
		}
		id := r.URL.Query().Get("serviceId")
		desired.Name = name + "/" + id
		desired.Uri = ServedAt(id)
		s.revisions[desired.Name] = 1
		desired.LatestReadyRevision = desired.Name + "/revisions/" + id + "-00001"
		s.Services[desired.Name] = desired
		WriteBody(w, done)
	case r.Method == http.MethodPatch:
		desired := ReadBody[runv2.GoogleCloudRunV2Service](w, r)
		current := s.Services[name]
		if desired == nil || current == nil {
			notFound(w)
			return
		}
		s.Patched = append(s.Patched, name)
		if mask := r.URL.Query().Get("updateMask"); mask != "" {
			if slices.Contains(strings.Split(mask, ","), "traffic") {
				current.Traffic = desired.Traffic
			}
			WriteBody(w, done)
			return
		}
		if !SameTemplate(current.Template, desired.Template) {
			s.revisions[name]++
			current.LatestReadyRevision = name + "/revisions/" + run.RevisionName(name) + "-0000" + strconv.Itoa(s.revisions[name])
		}
		current.Template, current.Traffic = desired.Template, desired.Traffic
		current.Ingress, current.InvokerIamDisabled = desired.Ingress, desired.InvokerIamDisabled
		WriteBody(w, done)
	case r.Method == http.MethodDelete:
		s.deleted = append(s.deleted, name)
		if s.Services[name] == nil {
			notFound(w)
			return
		}
		delete(s.Services, name)
		WriteBody(w, done)
	case s.Services[name] != nil:
		WriteBody(w, s.Services[name])
	default:
		notFound(w)
	}
}

func (s *Sync) serveScheduler(w http.ResponseWriter, r *http.Request, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	name, verb, _ := strings.Cut(name, ":")
	switch {
	case verb == "resume":
		current := s.Schedules[name]
		if current == nil {
			notFound(w)
			return
		}
		if current.State != "PAUSED" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"job is not paused"}}`))
			return
		}
		current.State = "ENABLED"
		WriteBody(w, current)
	case r.Method == http.MethodPost:
		asked := ReadBody[cloudscheduler.Job](w, r)
		if asked == nil {
			return
		}
		if s.Schedules[asked.Name] != nil {
			Conflicted(w, "already exists")
			return
		}
		asked.State = "ENABLED"
		s.Schedules[asked.Name] = asked
		WriteBody(w, asked)
	case r.Method == http.MethodPatch:
		asked := ReadBody[cloudscheduler.Job](w, r)
		if asked == nil {
			return
		}
		s.Patched = append(s.Patched, name)
		asked.Name = name
		asked.State = "ENABLED"
		if current := s.Schedules[name]; current != nil && current.State != "UPDATE_FAILED" {
			asked.State = current.State
		}
		s.Schedules[name] = asked
		WriteBody(w, asked)
	case r.Method == http.MethodDelete:
		s.deleted = append(s.deleted, name)
		if s.Schedules[name] == nil {
			notFound(w)
			return
		}
		delete(s.Schedules, name)
		_, _ = w.Write([]byte(`{}`))
	default:
		if s.Schedules[name] == nil {
			notFound(w)
			return
		}
		WriteBody(w, s.Schedules[name])
	}
}

func (s *Sync) Service(name string) *runv2.GoogleCloudRunV2Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Services[name]
}

func (s *Sync) Policy(name string) *runv2.GoogleIamV1Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Policies[name]
}

func (s *Sync) Schedule(name string) *cloudscheduler.Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Schedules[name]
}

func (s *Sync) Change(edit func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	edit()
}

func (s *Sync) WasPatched(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Contains(strings.Join(s.Patched, "\n"), name)
}

func Encoded(t *testing.T, body any) string {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
