package gcp

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	sqladmin "google.golang.org/api/sqladmin/v1"
)

const databaseAddress = "10.240.0.9"

type cloudSQLServer struct {
	mu         sync.Mutex
	instances  map[string]*sqladmin.DatabaseInstance
	databases  map[string][]string
	users      map[string]map[string]string
	created    []*sqladmin.DatabaseInstance
	sent       []map[string]any
	deleted    []string
	operations int
	listed     []string
	writes     []string
	addressed  bool
	primary    string
	refusedSet int
}

func newCloudSQLServer() *cloudSQLServer {
	return &cloudSQLServer{
		instances: map[string]*sqladmin.DatabaseInstance{},
		databases: map[string][]string{},
		users:     map[string]map[string]string{},
		addressed: true,
	}
}

func servingCloudSQL(t *testing.T) (*Provider, *cloudSQLServer) {
	t.Helper()
	server := newCloudSQLServer()
	served := httptest.NewServer(http.HandlerFunc(server.serve))
	t.Cleanup(served.Close)
	return pushing(t, served.URL), server
}

func (s *cloudSQLServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/projects/acme-prod/"), "/")
	switch {
	case parts[0] == "operations":
		writeBody(w, &sqladmin.Operation{Name: parts[1], Status: operationDone})
	case len(parts) == 1 && r.Method == http.MethodGet:
		s.list(w, r.URL.Query().Get("filter"))
	case len(parts) == 1 && r.Method == http.MethodPost:
		body, err := io.ReadAll(r.Body)
		created, sent := &sqladmin.DatabaseInstance{}, map[string]any{}
		if err != nil || json.Unmarshal(body, created) != nil || json.Unmarshal(body, &sent) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.created = append(s.created, created)
		s.sent = append(s.sent, sent)
		s.writes = append(s.writes, "create instance "+created.Name)
		stored := *created
		stored.State = "RUNNABLE"
		if s.addressed && stored.Settings != nil && stored.Settings.IpConfiguration != nil && stored.Settings.IpConfiguration.PscConfig != nil {
			for _, auto := range stored.Settings.IpConfiguration.PscConfig.PscAutoConnections {
				auto.IpAddress = databaseAddress
			}
		}
		if s.primary != "" {
			stored.IpAddresses = []*sqladmin.IpMapping{{Type: "PRIMARY", IpAddress: s.primary}}
		}
		s.instances[created.Name] = &stored
		s.operation(w)
	case len(parts) == 2:
		s.instance(w, r, parts[1])
	case len(parts) >= 3 && parts[2] == "databases":
		s.database(w, r, parts)
	case len(parts) >= 3 && parts[2] == "users":
		s.user(w, r, parts)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (s *cloudSQLServer) operation(w http.ResponseWriter) {
	s.operations++
	writeBody(w, &sqladmin.Operation{Name: fmt.Sprintf("operation-%d", s.operations), Status: "PENDING"})
}

func (s *cloudSQLServer) list(w http.ResponseWriter, filter string) {
	s.listed = append(s.listed, filter)
	listed := &sqladmin.InstancesListResponse{}
	for _, name := range slices.Sorted(maps.Keys(s.instances)) {
		instance := s.instances[name]
		matches := true
		for _, term := range strings.Fields(filter) {
			label, value, _ := strings.Cut(strings.TrimPrefix(term, "settings.userLabels."), ":")
			if instance.Settings == nil || instance.Settings.UserLabels[label] != value {
				matches = false
			}
		}
		if matches {
			listed.Items = append(listed.Items, instance)
		}
	}
	writeBody(w, listed)
}

func (s *cloudSQLServer) instance(w http.ResponseWriter, r *http.Request, name string) {
	instance := s.instances[name]
	if instance == nil {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":404,"message":"The Cloud SQL instance does not exist."}}`))
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeBody(w, instance)
	case http.MethodDelete:
		delete(s.instances, name)
		s.deleted = append(s.deleted, name)
		s.writes = append(s.writes, "delete instance "+name)
		s.operation(w)
	}
}

func (s *cloudSQLServer) database(w http.ResponseWriter, r *http.Request, parts []string) {
	instance := parts[1]
	switch {
	case r.Method == http.MethodGet && len(parts) == 4:
		if !slices.Contains(s.databases[instance], parts[3]) {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":404,"message":"database not found"}}`))
			return
		}
		writeBody(w, &sqladmin.Database{Name: parts[3], Instance: instance})
	case r.Method == http.MethodPost:
		created := readBody[sqladmin.Database](w, r)
		if created == nil {
			return
		}
		s.databases[instance] = append(s.databases[instance], created.Name)
		s.writes = append(s.writes, "create database "+created.Name)
		s.operation(w)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (s *cloudSQLServer) user(w http.ResponseWriter, r *http.Request, parts []string) {
	instance := parts[1]
	if s.users[instance] == nil {
		s.users[instance] = map[string]string{}
	}
	switch {
	case r.Method == http.MethodGet && len(parts) == 4:
		if _, found := s.users[instance][parts[3]]; !found {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":404,"message":"user not found"}}`))
			return
		}
		writeBody(w, &sqladmin.User{Name: parts[3], Instance: instance})
	case r.Method == http.MethodPost:
		created := readBody[sqladmin.User](w, r)
		if created == nil {
			return
		}
		s.users[instance][created.Name] = created.Password
		s.writes = append(s.writes, "create user "+created.Name)
		s.operation(w)
	case r.Method == http.MethodPut && s.refusedSet > 0:
		s.refusedSet--
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"code":400,"message":"the password was refused"}}`))
	case r.Method == http.MethodPut:
		updated := readBody[sqladmin.User](w, r)
		if updated == nil {
			return
		}
		s.users[instance][r.URL.Query().Get("name")] = updated.Password
		s.writes = append(s.writes, "update user "+r.URL.Query().Get("name"))
		s.operation(w)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (s *cloudSQLServer) holding(name string, labels map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.instances[name] = &sqladmin.DatabaseInstance{Name: name, State: "RUNNABLE", Settings: &sqladmin.Settings{UserLabels: labels}}
}

func (s *cloudSQLServer) filters() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.listed)
}

func (s *cloudSQLServer) wrote() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.writes)
}
