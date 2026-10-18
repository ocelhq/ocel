package gcp

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
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

type syncServer struct {
	*googletest.IAM

	mu        sync.Mutex
	services  map[string]*runv2.GoogleCloudRunV2Service
	revisions map[string]int
	policies  map[string]*runv2.GoogleIamV1Policy
	schedules map[string]*cloudscheduler.Job
	patched   []string
	deleted   []string
	pushed    []string

	policyChanges int
}

func newSyncServer() *syncServer {
	return &syncServer{
		IAM:       googletest.GrantedIAM(),
		services:  map[string]*runv2.GoogleCloudRunV2Service{},
		revisions: map[string]int{},
		policies:  map[string]*runv2.GoogleIamV1Policy{},
		schedules: map[string]*cloudscheduler.Job{},
	}
}

func servedAt(name string) string { return "https://" + name + "-3k2x7lq4ta-ew.a.run.app" }

func (s *syncServer) open(t *testing.T) bootstrap.Engine {
	t.Helper()
	iamRest := s.Rest(t)
	c := s.Serve(t, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasPrefix(path, "/v2/") && strings.Contains(path, "/services"):
			s.serveRun(w, r, strings.TrimPrefix(path, "/v2/"))
		case strings.HasPrefix(path, "/v2/") && strings.Contains(path, "/operations/"):
			googletest.WriteBody(w, &runv2.GoogleLongrunningOperation{Name: strings.TrimPrefix(path, "/v2/"), Done: true})
		case strings.HasPrefix(path, "/v1/") && strings.Contains(path, "/jobs"):
			s.serveScheduler(w, r, strings.TrimPrefix(path, "/v1/"))
		default:
			iamRest(w, r)
		}
	})
	p := pushing(t, c.Endpoint)
	p.resolved = c
	return composed(bootstrap.Engine{
		Clients:        c,
		DeployAndRoute: p.services().DeployAndRoute,
		TearDown:       p.services().TearDown,
		PushBinary: func(_ context.Context, _ environment.Tier, _, ref string, _ []byte, _ string) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.pushed = append(s.pushed, ref)
			return nil
		},
	})
}

func notFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
}

func (s *syncServer) serveRun(w http.ResponseWriter, r *http.Request, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	name, verb, _ := strings.Cut(path, ":")
	done := &runv2.GoogleLongrunningOperation{Name: "projects/acme-prod/locations/europe-west1/operations/op", Done: true}
	switch {
	case verb == "getIamPolicy":
		if policy := s.policies[name]; policy != nil {
			googletest.WriteBody(w, policy)
			return
		}
		googletest.WriteBody(w, &runv2.GoogleIamV1Policy{Etag: "BwXhoLA="})
	case verb == "setIamPolicy":
		asked := googletest.ReadBody[runv2.GoogleIamV1SetIamPolicyRequest](w, r)
		if asked == nil {
			return
		}
		if s.policyChanges > 0 {
			s.policyChanges--
			w.WriteHeader(http.StatusPreconditionFailed)
			w.Write([]byte(`{"error":{"code":412,"message":"conditionNotMet"}}`))
			return
		}
		s.policies[name] = asked.Policy
		googletest.WriteBody(w, asked.Policy)
	case r.Method == http.MethodPost:
		desired := googletest.ReadBody[runv2.GoogleCloudRunV2Service](w, r)
		if desired == nil {
			return
		}
		id := r.URL.Query().Get("serviceId")
		desired.Name = name + "/" + id
		desired.Uri = servedAt(id)
		s.revisions[desired.Name] = 1
		desired.LatestReadyRevision = desired.Name + "/revisions/" + id + "-00001"
		s.services[desired.Name] = desired
		googletest.WriteBody(w, done)
	case r.Method == http.MethodPatch:
		desired := googletest.ReadBody[runv2.GoogleCloudRunV2Service](w, r)
		current := s.services[name]
		if desired == nil || current == nil {
			notFound(w)
			return
		}
		s.patched = append(s.patched, name)
		if mask := r.URL.Query().Get("updateMask"); mask != "" {
			if slices.Contains(strings.Split(mask, ","), "traffic") {
				current.Traffic = desired.Traffic
			}
			googletest.WriteBody(w, done)
			return
		}
		if !googletest.SameTemplate(current.Template, desired.Template) {
			s.revisions[name]++
			current.LatestReadyRevision = name + "/revisions/" + run.RevisionName(name) + "-0000" + strconv.Itoa(s.revisions[name])
		}
		current.Template, current.Traffic = desired.Template, desired.Traffic
		current.Ingress, current.InvokerIamDisabled = desired.Ingress, desired.InvokerIamDisabled
		googletest.WriteBody(w, done)
	case r.Method == http.MethodDelete:
		s.deleted = append(s.deleted, name)
		if s.services[name] == nil {
			notFound(w)
			return
		}
		delete(s.services, name)
		googletest.WriteBody(w, done)
	case s.services[name] != nil:
		googletest.WriteBody(w, s.services[name])
	default:
		notFound(w)
	}
}

func (s *syncServer) serveScheduler(w http.ResponseWriter, r *http.Request, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	name, verb, _ := strings.Cut(name, ":")
	switch {
	case verb == "resume":
		current := s.schedules[name]
		if current == nil {
			notFound(w)
			return
		}
		if current.State != "PAUSED" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"job is not paused"}}`))
			return
		}
		current.State = "ENABLED"
		googletest.WriteBody(w, current)
	case r.Method == http.MethodPost:
		asked := googletest.ReadBody[cloudscheduler.Job](w, r)
		if asked == nil {
			return
		}
		if s.schedules[asked.Name] != nil {
			googletest.Conflicted(w, "already exists")
			return
		}
		asked.State = "ENABLED"
		s.schedules[asked.Name] = asked
		googletest.WriteBody(w, asked)
	case r.Method == http.MethodPatch:
		asked := googletest.ReadBody[cloudscheduler.Job](w, r)
		if asked == nil {
			return
		}
		s.patched = append(s.patched, name)
		asked.Name = name
		asked.State = "ENABLED"
		if current := s.schedules[name]; current != nil && current.State != "UPDATE_FAILED" {
			asked.State = current.State
		}
		s.schedules[name] = asked
		googletest.WriteBody(w, asked)
	case r.Method == http.MethodDelete:
		s.deleted = append(s.deleted, name)
		if s.schedules[name] == nil {
			notFound(w)
			return
		}
		delete(s.schedules, name)
		w.Write([]byte(`{}`))
	default:
		if s.schedules[name] == nil {
			notFound(w)
			return
		}
		googletest.WriteBody(w, s.schedules[name])
	}
}

func (s *syncServer) service(name string) *runv2.GoogleCloudRunV2Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.services[name]
}

func (s *syncServer) policy(name string) *runv2.GoogleIamV1Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.policies[name]
}

func (s *syncServer) schedule(name string) *cloudscheduler.Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.schedules[name]
}

func (s *syncServer) change(edit func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	edit()
}

func (s *syncServer) wasPatched(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Contains(strings.Join(s.patched, "\n"), name)
}

func encoded(t *testing.T, body any) string {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
