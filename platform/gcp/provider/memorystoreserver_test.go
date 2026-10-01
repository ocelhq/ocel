package gcp

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const (
	storeAddress   = "10.240.0.5"
	storeToken     = "memorystore-generated-token"
	storeAuthority = "-----BEGIN CERTIFICATE-----\nroot\n-----END CERTIFICATE-----\n"
	storeOperation = "projects/acme-prod/locations/europe-west1/operations/operation-1"
	createdAt      = "2026-10-01T10:00:00Z"
	endedAt        = "2026-10-01T10:14:30Z"
)

type memorystoreServer struct {
	mu         sync.Mutex
	instances  map[string]*memorystoreInstance
	created    []*memorystoreInstance
	patched    []string
	deleted    []string
	polls      int
	reads      int
	unnamed    bool
	listed     []string
	throttles  int
	emptyPages int
	failed     string

	throttledWrites int
	writeIDs        []string
	settling        map[string]int
	states          map[string]string
}

func servingMemorystore(t *testing.T) (*Provider, *memorystoreServer) {
	t.Helper()
	server, url := startMemorystore(t)
	return pushing(t, url), server
}

func startMemorystore(t *testing.T) (*memorystoreServer, string) {
	t.Helper()
	server := &memorystoreServer{instances: map[string]*memorystoreInstance{}}
	served := httptest.NewServer(server.serve(t))
	t.Cleanup(served.Close)
	return server, served.URL
}

func (s *memorystoreServer) serve(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if s.throttles > 0 {
			s.throttles--
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"code":429,"message":"quota"}}`))
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/v1/")
		if r.Method != http.MethodGet {
			s.writeIDs = append(s.writeIDs, r.URL.Query().Get("requestId"))
			if s.throttledWrites > 0 {
				s.throttledWrites--
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte(`{"error":{"code":503,"message":"try again"}}`))
				return
			}
		}
		switch {
		case r.Method == http.MethodGet && strings.Contains(name, "/operations/"):
			s.polls++
			writeBody(w, s.operation(true))
		case r.Method == http.MethodGet && strings.HasSuffix(name, "/certificateAuthority"):
			writeBody(w, map[string]any{"managedServerCa": map[string]any{"caCerts": []any{
				map[string]any{"certificates": []string{storeAuthority}},
			}}})
		case r.Method == http.MethodGet && strings.HasSuffix(name, "/tokenAuthUsers/default/authTokens"):
			writeBody(w, map[string]any{"authTokens": []any{
				map[string]any{"name": name + "/first", "token": "older-token", "state": "ACTIVE", "createTime": createdAt},
				map[string]any{"name": name + "/newer", "token": storeToken, "state": "ACTIVE", "createTime": endedAt},
				map[string]any{"name": name + "/adding", "token": "adding-token", "state": "CREATING", "createTime": "2026-10-01T11:00:00Z"},
			}})
		case r.Method == http.MethodGet && strings.HasSuffix(name, "/instances"):
			s.list(w, r.URL.Query())
		case r.Method == http.MethodGet:
			s.reads++
			instance, found := s.instances[name]
			if !found {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
				return
			}
			if s.settling[name] > 0 {
				s.settling[name]--
				writeBody(w, &memorystoreInstance{Name: name, State: "CREATING"})
				return
			}
			if state, held := s.states[name]; held {
				writeBody(w, &memorystoreInstance{Name: name, State: state})
				return
			}
			writeBody(w, instance)
		case r.Method == http.MethodPost && strings.HasSuffix(name, "/instances"):
			created := readBody[memorystoreInstance](w, r)
			if created == nil {
				return
			}
			s.created = append(s.created, created)
			stored := *created
			stored.Name = name + "/" + r.URL.Query().Get("instanceId")
			stored.State = instanceActive
			stored.Endpoints = []instanceEndpoint{{Connections: []endpointConnection{
				{PSCAutoConnection: &pscAutoConnection{IPAddress: storeAddress, Port: 6379, ConnectionType: primaryEndpoint}},
			}}}
			s.instances[stored.Name] = &stored
			writeBody(w, s.operation(false))
		case r.Method == http.MethodPatch:
			changed := readBody[memorystoreInstance](w, r)
			if changed == nil {
				return
			}
			s.patched = append(s.patched, r.URL.Query().Get("updateMask"))
			if stored := s.instances[name]; stored != nil {
				stored.NodeType, stored.EngineVersion, stored.EngineConfigs = changed.NodeType, changed.EngineVersion, changed.EngineConfigs
			}
			writeBody(w, s.operation(false))
		case r.Method == http.MethodDelete:
			if _, found := s.instances[name]; !found {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
				return
			}
			delete(s.instances, name)
			s.deleted = append(s.deleted, name)
			writeBody(w, s.operation(false))
		default:
			t.Errorf("Memorystore was called %s %s, which nothing here serves", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func (s *memorystoreServer) stillCreating(name string, reads int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settling == nil {
		s.settling = map[string]int{}
	}
	s.settling[name] = reads
}

var labelFilter = regexp.MustCompile(`labels\.([a-z-]+)="([^"]*)"`)

func (s *memorystoreServer) list(w http.ResponseWriter, query url.Values) {
	s.listed = append(s.listed, query.Get("filter"))
	page, _ := strconv.Atoi(strings.TrimPrefix(query.Get("pageToken"), "page-"))
	if page < s.emptyPages {
		writeBody(w, map[string]any{"instances": []any{}, "nextPageToken": "page-" + strconv.Itoa(page+1)})
		return
	}
	var matching []*memorystoreInstance
	for _, name := range slices.Sorted(maps.Keys(s.instances)) {
		instance := s.instances[name]
		if !slices.ContainsFunc(labelFilter.FindAllStringSubmatch(query.Get("filter"), -1), func(label []string) bool {
			return instance.Labels[label[1]] != label[2]
		}) {
			matching = append(matching, instance)
		}
	}
	writeBody(w, map[string]any{"instances": matching})
}

func (s *memorystoreServer) holding(name string, labels map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.instances[name] = &memorystoreInstance{Name: name, State: instanceActive, Labels: labels}
}

func (s *memorystoreServer) filters() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.listed...)
}

func (s *memorystoreServer) heldIn(name, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states == nil {
		s.states = map[string]string{}
	}
	s.states[name] = state
}

func (s *memorystoreServer) instanceReads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

func (s *memorystoreServer) requestIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writeIDs...)
}

func (s *memorystoreServer) operation(done bool) *memorystoreOperation {
	if s.unnamed && !done {
		return &memorystoreOperation{}
	}
	operation := &memorystoreOperation{Name: storeOperation, Done: done, Metadata: &operationMetadata{CreateTime: createdAt}}
	if done {
		operation.Metadata.EndTime = endedAt
		if s.failed != "" {
			operation.Error = &operationStatus{Code: 9, Message: s.failed}
		}
	}
	return operation
}

func (s *memorystoreServer) instance(name string) *memorystoreInstance {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.instances[name]
}

func (s *memorystoreServer) creates() []*memorystoreInstance {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.created
}

func (s *memorystoreServer) masks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.patched
}

func (s *memorystoreServer) deletes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleted
}

func asJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
