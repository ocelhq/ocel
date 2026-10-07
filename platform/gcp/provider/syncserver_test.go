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
	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/environment"
)

type syncServer struct {
	*iamServer

	mu        sync.Mutex
	services  map[string]*run.GoogleCloudRunV2Service
	revisions map[string]int
	policies  map[string]*run.GoogleIamV1Policy
	schedules map[string]*cloudscheduler.Job
	patched   []string
	deleted   []string
	pushed    []string

	policyChanges int
}

func newSyncServer() *syncServer {
	return &syncServer{
		iamServer: grantedIAM(),
		services:  map[string]*run.GoogleCloudRunV2Service{},
		revisions: map[string]int{},
		policies:  map[string]*run.GoogleIamV1Policy{},
		schedules: map[string]*cloudscheduler.Job{},
	}
}

func servedAt(name string) string { return "https://" + name + "-3k2x7lq4ta-ew.a.run.app" }

func (s *syncServer) open(t *testing.T) bootstrap {
	t.Helper()
	iamRest := s.rest(t)
	c := s.serve(t, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasPrefix(path, "/v2/") && strings.Contains(path, "/services"):
			s.serveRun(w, r, strings.TrimPrefix(path, "/v2/"))
		case strings.HasPrefix(path, "/v2/") && strings.Contains(path, "/operations/"):
			writeBody(w, &run.GoogleLongrunningOperation{Name: strings.TrimPrefix(path, "/v2/"), Done: true})
		case strings.HasPrefix(path, "/v1/") && strings.Contains(path, "/jobs"):
			s.serveScheduler(w, r, strings.TrimPrefix(path, "/v1/"))
		default:
			iamRest(w, r)
		}
	})
	p := pushing(t, c.endpoint)
	p.resolved = c
	return bootstrap{
		clients:        c,
		deployAndRoute: p.deployAndRoute,
		tearDown:       p.tearDown,
		pushBinary: func(_ context.Context, _ environment.Tier, _, ref string, _ []byte, _ string) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.pushed = append(s.pushed, ref)
			return nil
		},
	}
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
	done := &run.GoogleLongrunningOperation{Name: "projects/acme-prod/locations/europe-west1/operations/op", Done: true}
	switch {
	case verb == "getIamPolicy":
		if policy := s.policies[name]; policy != nil {
			writeBody(w, policy)
			return
		}
		writeBody(w, &run.GoogleIamV1Policy{Etag: "BwXhoLA="})
	case verb == "setIamPolicy":
		asked := readBody[run.GoogleIamV1SetIamPolicyRequest](w, r)
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
		writeBody(w, asked.Policy)
	case r.Method == http.MethodPost:
		desired := readBody[run.GoogleCloudRunV2Service](w, r)
		if desired == nil {
			return
		}
		id := r.URL.Query().Get("serviceId")
		desired.Name = name + "/" + id
		desired.Uri = servedAt(id)
		s.revisions[desired.Name] = 1
		desired.LatestReadyRevision = desired.Name + "/revisions/" + id + "-00001"
		s.services[desired.Name] = desired
		writeBody(w, done)
	case r.Method == http.MethodPatch:
		desired := readBody[run.GoogleCloudRunV2Service](w, r)
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
			writeBody(w, done)
			return
		}
		if !sameTemplate(current.Template, desired.Template) {
			s.revisions[name]++
			current.LatestReadyRevision = name + "/revisions/" + revisionName(name) + "-0000" + strconv.Itoa(s.revisions[name])
		}
		current.Template, current.Traffic = desired.Template, desired.Traffic
		current.Ingress, current.InvokerIamDisabled = desired.Ingress, desired.InvokerIamDisabled
		writeBody(w, done)
	case r.Method == http.MethodDelete:
		s.deleted = append(s.deleted, name)
		if s.services[name] == nil {
			notFound(w)
			return
		}
		delete(s.services, name)
		writeBody(w, done)
	case s.services[name] != nil:
		writeBody(w, s.services[name])
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
		writeBody(w, current)
	case r.Method == http.MethodPost:
		asked := readBody[cloudscheduler.Job](w, r)
		if asked == nil {
			return
		}
		if s.schedules[asked.Name] != nil {
			conflicted(w, "already exists")
			return
		}
		asked.State = "ENABLED"
		s.schedules[asked.Name] = asked
		writeBody(w, asked)
	case r.Method == http.MethodPatch:
		asked := readBody[cloudscheduler.Job](w, r)
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
		writeBody(w, asked)
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
		writeBody(w, s.schedules[name])
	}
}

func (s *syncServer) service(name string) *run.GoogleCloudRunV2Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.services[name]
}

func (s *syncServer) policy(name string) *run.GoogleIamV1Policy {
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
