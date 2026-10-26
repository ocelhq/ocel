package googletest

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const (
	StoreAddress   = "10.240.0.5"
	StoreToken     = "memorystore-generated-token"
	StoreAuthority = "-----BEGIN CERTIFICATE-----\nroot\n-----END CERTIFICATE-----\n"
	storeOperation = "projects/acme-prod/locations/europe-west1/operations/operation-1"
	CreatedAt      = "2026-10-01T10:00:00Z"
	EndedAt        = "2026-10-01T10:14:30Z"
)

type Memorystore struct {
	mu         sync.Mutex
	instances  map[string]*gcloud.MemorystoreInstance
	created    []*gcloud.MemorystoreInstance
	patched    []string
	deleted    []string
	polls      int
	reads      int
	Unnamed    bool
	listed     []string
	Throttles  int
	EmptyPages int
	Failed     string

	ThrottledWrites int
	writeIDs        []string
	settling        map[string]int
	states          map[string]string
	networks        map[string]bool
}

func StartMemorystore(t *testing.T) (*Memorystore, string) {
	t.Helper()
	server := &Memorystore{instances: map[string]*gcloud.MemorystoreInstance{}}
	served := httptest.NewServer(server.serve(t))
	t.Cleanup(served.Close)
	return server, served.URL
}

func (s *Memorystore) serve(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if s.Throttles > 0 {
			s.Throttles--
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"quota"}}`))
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/v1/")
		if r.Method != http.MethodGet {
			s.writeIDs = append(s.writeIDs, r.URL.Query().Get("requestId"))
			if s.ThrottledWrites > 0 {
				s.ThrottledWrites--
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":{"code":503,"message":"try again"}}`))
				return
			}
		}
		switch {
		case r.Method == http.MethodGet && strings.Contains(name, "/global/networks/"):
			if !s.networks[path.Base(name)] {
				WriteNotFound(w)
				return
			}
			WriteBody(w, map[string]any{"name": path.Base(name)})
		case r.Method == http.MethodGet && strings.Contains(name, "/operations/"):
			s.polls++
			WriteBody(w, s.operation(true))
		case r.Method == http.MethodGet && strings.HasSuffix(name, "/certificateAuthority"):
			WriteBody(w, map[string]any{"managedServerCa": map[string]any{"caCerts": []any{
				map[string]any{"certificates": []string{StoreAuthority}},
			}}})
		case r.Method == http.MethodGet && strings.HasSuffix(name, "/tokenAuthUsers/default/authTokens"):
			WriteBody(w, map[string]any{"authTokens": []any{
				map[string]any{"name": name + "/first", "token": "older-token", "state": "ACTIVE", "createTime": CreatedAt},
				map[string]any{"name": name + "/newer", "token": StoreToken, "state": "ACTIVE", "createTime": EndedAt},
				map[string]any{"name": name + "/adding", "token": "adding-token", "state": "CREATING", "createTime": "2026-10-01T11:00:00Z"},
			}})
		case r.Method == http.MethodGet && strings.HasSuffix(name, "/instances"):
			s.list(w, r.URL.Query())
		case r.Method == http.MethodGet:
			s.reads++
			instance, found := s.instances[name]
			if !found {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
				return
			}
			if s.settling[name] > 0 {
				s.settling[name]--
				WriteBody(w, &gcloud.MemorystoreInstance{Name: name, State: "CREATING"})
				return
			}
			if state, held := s.states[name]; held {
				WriteBody(w, &gcloud.MemorystoreInstance{Name: name, State: state})
				return
			}
			WriteBody(w, instance)
		case r.Method == http.MethodPost && strings.HasSuffix(name, "/instances"):
			created := ReadBody[gcloud.MemorystoreInstance](w, r)
			if created == nil {
				return
			}
			s.created = append(s.created, created)
			stored := *created
			stored.Name = name + "/" + r.URL.Query().Get("instanceId")
			stored.State = gcloud.InstanceActive
			stored.Endpoints = []gcloud.InstanceEndpoint{{Connections: []gcloud.EndpointConnection{
				{PSCAutoConnection: &gcloud.PSCAutoConnection{IPAddress: StoreAddress, Port: 6379, ConnectionType: gcloud.PrimaryEndpoint, Network: connectedNetwork(created)}},
			}}}
			s.instances[stored.Name] = &stored
			WriteBody(w, s.operation(false))
		case r.Method == http.MethodPatch:
			changed := ReadBody[gcloud.MemorystoreInstance](w, r)
			if changed == nil {
				return
			}
			mask := r.URL.Query().Get("updateMask")
			s.patched = append(s.patched, mask)
			if stored := s.instances[name]; stored != nil {
				for _, field := range strings.Split(mask, ",") {
					switch field {
					case "node_type":
						stored.NodeType = changed.NodeType
					case "engine_version":
						stored.EngineVersion = changed.EngineVersion
					case "engine_configs":
						stored.EngineConfigs = changed.EngineConfigs
					case "replica_count":
						stored.ReplicaCount = changed.ReplicaCount
					case "maintenance_policy":
						stored.MaintenancePolicy = changed.MaintenancePolicy
					case "automated_backup_config":
						stored.AutomatedBackupConfig = changed.AutomatedBackupConfig
					case "deletion_protection_enabled":
						stored.DeletionProtectionEnabled = changed.DeletionProtectionEnabled
					case "labels":
						stored.Labels = changed.Labels
					default:
						t.Errorf("Memorystore was asked to update %q, which nothing here updates", field)
					}
				}
			}
			WriteBody(w, s.operation(false))
		case r.Method == http.MethodDelete:
			held, found := s.instances[name]
			if !found {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
				return
			}
			if held.DeletionProtectionEnabled != nil && *held.DeletionProtectionEnabled {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":400,"message":"the instance is protected from deletion"}}`))
				return
			}
			delete(s.instances, name)
			s.deleted = append(s.deleted, name)
			WriteBody(w, s.operation(false))
		default:
			t.Errorf("Memorystore was called %s %s, which nothing here serves", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func connectedNetwork(created *gcloud.MemorystoreInstance) string {
	for _, endpoint := range created.Endpoints {
		for _, connection := range endpoint.Connections {
			if connection.PSCAutoConnection != nil {
				return connection.PSCAutoConnection.Network
			}
		}
	}
	return ""
}

func (s *Memorystore) HoldNetwork(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.networks == nil {
		s.networks = map[string]bool{}
	}
	s.networks[name] = true
}

func (s *Memorystore) StillCreating(name string, reads int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settling == nil {
		s.settling = map[string]int{}
	}
	s.settling[name] = reads
}

var labelFilter = regexp.MustCompile(`labels\.([a-z-]+)="([^"]*)"`)

func (s *Memorystore) list(w http.ResponseWriter, query url.Values) {
	s.listed = append(s.listed, query.Get("filter"))
	page, _ := strconv.Atoi(strings.TrimPrefix(query.Get("pageToken"), "page-"))
	if page < s.EmptyPages {
		WriteBody(w, map[string]any{"instances": []any{}, "nextPageToken": "page-" + strconv.Itoa(page+1)})
		return
	}
	var matching []*gcloud.MemorystoreInstance
	for _, name := range slices.Sorted(maps.Keys(s.instances)) {
		instance := s.instances[name]
		if !slices.ContainsFunc(labelFilter.FindAllStringSubmatch(query.Get("filter"), -1), func(label []string) bool {
			return instance.Labels[label[1]] != label[2]
		}) {
			matching = append(matching, instance)
		}
	}
	WriteBody(w, map[string]any{"instances": matching})
}

func (s *Memorystore) Holding(name string, labels map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.instances[name] = &gcloud.MemorystoreInstance{Name: name, State: gcloud.InstanceActive, Labels: labels}
}

func (s *Memorystore) Filters() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.listed...)
}

func (s *Memorystore) HeldWithReplicas(name string, replicas int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.instances[name].ReplicaCount = &replicas
}

func (s *Memorystore) HeldIn(name, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states == nil {
		s.states = map[string]string{}
	}
	s.states[name] = state
}

func (s *Memorystore) InstanceReads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

func (s *Memorystore) RequestIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writeIDs...)
}

func (s *Memorystore) operation(done bool) *gcloud.MemorystoreOperation {
	if s.Unnamed && !done {
		return &gcloud.MemorystoreOperation{}
	}
	operation := &gcloud.MemorystoreOperation{Name: storeOperation, Done: done, Metadata: &gcloud.OperationMetadata{CreateTime: CreatedAt}}
	if done {
		operation.Metadata.EndTime = EndedAt
		if s.Failed != "" {
			operation.Error = &gcloud.OperationStatus{Code: 9, Message: s.Failed}
		}
	}
	return operation
}

func (s *Memorystore) Instance(name string) *gcloud.MemorystoreInstance {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.instances[name]
}

func (s *Memorystore) Creates() []*gcloud.MemorystoreInstance {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.created
}

func (s *Memorystore) Masks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.patched
}

func (s *Memorystore) Deletes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleted
}
