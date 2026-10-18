package googletest

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	sqladmin "google.golang.org/api/sqladmin/v1"
)

const DatabaseAddress = "10.240.0.9"

type CloudSQL struct {
	mu         sync.Mutex
	Instances  map[string]*sqladmin.DatabaseInstance
	Databases  map[string][]string
	Users      map[string]map[string]string
	Created    []*sqladmin.DatabaseInstance
	Sent       []map[string]any
	Deleted    []string
	Patched    []map[string]any
	operations int
	listed     []string
	writes     []string
	Addressed  bool
	Primary    string
	RefusedSet int
}

func NewCloudSQL() *CloudSQL {
	return &CloudSQL{
		Instances: map[string]*sqladmin.DatabaseInstance{},
		Databases: map[string][]string{},
		Users:     map[string]map[string]string{},
		Addressed: true,
	}
}

func (s *CloudSQL) Serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/projects/acme-prod/"), "/")
	switch {
	case parts[0] == "operations":
		WriteBody(w, &sqladmin.Operation{Name: parts[1], Status: gcloud.OperationDone})
	case len(parts) == 1 && r.Method == http.MethodGet:
		s.list(w, r.URL.Query().Get("filter"))
	case len(parts) == 1 && r.Method == http.MethodPost:
		body, err := io.ReadAll(r.Body)
		created, sent := &sqladmin.DatabaseInstance{}, map[string]any{}
		if err != nil || json.Unmarshal(body, created) != nil || json.Unmarshal(body, &sent) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.Created = append(s.Created, created)
		s.Sent = append(s.Sent, sent)
		s.writes = append(s.writes, "create instance "+created.Name)
		stored := *created
		stored.State = "RUNNABLE"
		if s.Addressed && stored.Settings != nil && stored.Settings.IpConfiguration != nil && stored.Settings.IpConfiguration.PscConfig != nil {
			for _, auto := range stored.Settings.IpConfiguration.PscConfig.PscAutoConnections {
				auto.IpAddress = DatabaseAddress
			}
		}
		if s.Primary != "" {
			stored.IpAddresses = []*sqladmin.IpMapping{{Type: "PRIMARY", IpAddress: s.Primary}}
		}
		s.Instances[created.Name] = &stored
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

func (s *CloudSQL) operation(w http.ResponseWriter) {
	s.operations++
	WriteBody(w, &sqladmin.Operation{Name: fmt.Sprintf("operation-%d", s.operations), Status: "PENDING"})
}

func (s *CloudSQL) list(w http.ResponseWriter, filter string) {
	s.listed = append(s.listed, filter)
	listed := &sqladmin.InstancesListResponse{}
	for _, name := range slices.Sorted(maps.Keys(s.Instances)) {
		instance := s.Instances[name]
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
	WriteBody(w, listed)
}

func (s *CloudSQL) instance(w http.ResponseWriter, r *http.Request, name string) {
	instance := s.Instances[name]
	if instance == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"The Cloud SQL instance does not exist."}}`))
		return
	}
	switch r.Method {
	case http.MethodGet:
		WriteBody(w, instance)
	case http.MethodPatch:
		s.patch(w, r, instance)
	case http.MethodDelete:
		if instance.Settings != nil && instance.Settings.DeletionProtectionEnabled {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"The instance is protected from deletion."}}`))
			return
		}
		delete(s.Instances, name)
		s.Deleted = append(s.Deleted, name)
		s.writes = append(s.writes, "delete instance "+name)
		s.operation(w)
	}
}

func (s *CloudSQL) patch(w http.ResponseWriter, r *http.Request, instance *sqladmin.DatabaseInstance) {
	body, err := io.ReadAll(r.Body)
	asked, sent := &sqladmin.DatabaseInstance{}, map[string]any{}
	if err != nil || json.Unmarshal(body, asked) != nil || json.Unmarshal(body, &sent) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.Patched = append(s.Patched, sent)
	s.writes = append(s.writes, "patch instance "+instance.Name)
	settings, _ := sent["settings"].(map[string]any)
	if instance.Settings == nil {
		instance.Settings = &sqladmin.Settings{}
	}
	if _, named := settings["tier"]; named {
		instance.Settings.Tier = asked.Settings.Tier
	}
	if _, named := settings["availabilityType"]; named {
		instance.Settings.AvailabilityType = asked.Settings.AvailabilityType
	}
	if _, named := settings["backupConfiguration"]; named {
		instance.Settings.BackupConfiguration = asked.Settings.BackupConfiguration
	}
	if _, named := settings["deletionProtectionEnabled"]; named {
		instance.Settings.DeletionProtectionEnabled = asked.Settings.DeletionProtectionEnabled
	}
	s.operation(w)
}

func (s *CloudSQL) database(w http.ResponseWriter, r *http.Request, parts []string) {
	instance := parts[1]
	switch {
	case r.Method == http.MethodGet && len(parts) == 4:
		if !slices.Contains(s.Databases[instance], parts[3]) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"database not found"}}`))
			return
		}
		WriteBody(w, &sqladmin.Database{Name: parts[3], Instance: instance})
	case r.Method == http.MethodPost:
		created := ReadBody[sqladmin.Database](w, r)
		if created == nil {
			return
		}
		s.Databases[instance] = append(s.Databases[instance], created.Name)
		s.writes = append(s.writes, "create database "+created.Name)
		s.operation(w)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (s *CloudSQL) user(w http.ResponseWriter, r *http.Request, parts []string) {
	instance := parts[1]
	if s.Users[instance] == nil {
		s.Users[instance] = map[string]string{}
	}
	switch {
	case r.Method == http.MethodGet && len(parts) == 4:
		if _, found := s.Users[instance][parts[3]]; !found {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"user not found"}}`))
			return
		}
		WriteBody(w, &sqladmin.User{Name: parts[3], Instance: instance})
	case r.Method == http.MethodPost:
		created := ReadBody[sqladmin.User](w, r)
		if created == nil {
			return
		}
		s.Users[instance][created.Name] = created.Password
		s.writes = append(s.writes, "create user "+created.Name)
		s.operation(w)
	case r.Method == http.MethodPut && s.RefusedSet > 0:
		s.RefusedSet--
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"the password was refused"}}`))
	case r.Method == http.MethodPut:
		updated := ReadBody[sqladmin.User](w, r)
		if updated == nil {
			return
		}
		s.Users[instance][r.URL.Query().Get("name")] = updated.Password
		s.writes = append(s.writes, "update user "+r.URL.Query().Get("name"))
		s.operation(w)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (s *CloudSQL) Holding(name string, labels map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Instances[name] = &sqladmin.DatabaseInstance{Name: name, State: "RUNNABLE", Settings: &sqladmin.Settings{UserLabels: labels}}
}

func (s *CloudSQL) Filters() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.listed)
}

func (s *CloudSQL) Wrote() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.writes)
}
