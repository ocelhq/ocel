package gcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/networkconnectivity/v1"
)

const serviceAgent = "serviceAccount:service-123456789@serverless-robot-prod.iam.gserviceaccount.com"

type networkServer struct {
	mu       sync.Mutex
	networks map[string]*compute.Network
	subnets  map[string]*compute.Subnetwork
	policies map[string]*networkconnectivity.ServiceConnectionPolicy
	iam      map[string]*compute.Policy
	writes   []string
}

func servingNetworks(t *testing.T) (bootstrap, *networkServer) {
	t.Helper()
	server := &networkServer{
		networks: map[string]*compute.Network{},
		subnets:  map[string]*compute.Subnetwork{},
		policies: map[string]*networkconnectivity.ServiceConnectionPolicy{},
		iam:      map[string]*compute.Policy{},
	}
	served := httptest.NewServer(server.serve(t))
	t.Cleanup(served.Close)
	names := Names{namespace: "ocel", project: "acme-prod"}
	registry := &frontRegistry{front: &countingFront{}}
	return bootstrap{clients: &clients{Names: names, region: "europe-west1", endpoint: served.URL}, fronts: registry}, server
}

func (s *networkServer) serve(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		at := r.URL.Path
		name := path.Base(at)
		switch {
		case strings.Contains(at, "/operations/"):
			writeBody(w, map[string]any{"name": at, "status": operationDone, "done": true})
		case r.Method == http.MethodGet && strings.HasSuffix(at, "/v1/projects/acme-prod"):
			writeBody(w, map[string]any{"projectId": "acme-prod", "projectNumber": "123456789"})
		case strings.HasSuffix(at, "/getIamPolicy"):
			s.readPolicy(w, path.Base(path.Dir(at)))
		case strings.HasSuffix(at, "/setIamPolicy"):
			s.writePolicy(w, r, path.Base(path.Dir(at)))
		case strings.Contains(at, "/serviceConnectionPolicies"):
			s.policy(w, r, name)
		case strings.Contains(at, "/subnetworks"):
			s.subnet(w, r, name)
		case strings.Contains(at, "/global/networks"):
			s.network(w, r, name)
		default:
			t.Errorf("the network was asked %s %s, which nothing here serves", r.Method, at)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func (s *networkServer) network(w http.ResponseWriter, r *http.Request, name string) {
	switch r.Method {
	case http.MethodGet:
		if found := s.networks[name]; found != nil {
			writeBody(w, found)
			return
		}
		missing(w)
	case http.MethodPost:
		said := readBody[map[string]any](w, r)
		if said == nil {
			return
		}
		encoded, _ := json.Marshal(*said)
		created := &compute.Network{}
		if err := json.Unmarshal(encoded, created); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if custom, sent := (*said)["autoCreateSubnetworks"]; sent && custom == false {
			created.ForceSendFields = []string{"AutoCreateSubnetworks"}
		}
		created.SelfLink = "https://www.googleapis.com/compute/v1/projects/acme-prod/global/networks/" + created.Name
		s.networks[created.Name] = created
		s.writes = append(s.writes, "create network "+created.Name)
		writeBody(w, &compute.Operation{Name: "op-network", Status: operationDone})
	case http.MethodDelete:
		if s.networks[name] == nil {
			missing(w)
			return
		}
		delete(s.networks, name)
		s.writes = append(s.writes, "delete network "+name)
		writeBody(w, &compute.Operation{Name: "op-network", Status: operationDone})
	}
}

func (s *networkServer) subnet(w http.ResponseWriter, r *http.Request, name string) {
	switch r.Method {
	case http.MethodGet:
		if found := s.subnets[name]; found != nil {
			writeBody(w, found)
			return
		}
		missing(w)
	case http.MethodPost:
		created := readBody[compute.Subnetwork](w, r)
		if created == nil {
			return
		}
		s.subnets[created.Name] = created
		s.writes = append(s.writes, "create subnetwork "+created.Name)
		writeBody(w, &compute.Operation{Name: "op-subnet", Status: operationDone})
	case http.MethodDelete:
		if s.subnets[name] == nil {
			missing(w)
			return
		}
		delete(s.subnets, name)
		s.writes = append(s.writes, "delete subnetwork "+name)
		writeBody(w, &compute.Operation{Name: "op-subnet", Status: operationDone})
	}
}

func (s *networkServer) policy(w http.ResponseWriter, r *http.Request, name string) {
	switch r.Method {
	case http.MethodGet:
		if found := s.policies[name]; found != nil {
			writeBody(w, found)
			return
		}
		missing(w)
	case http.MethodPost:
		created := readBody[networkconnectivity.ServiceConnectionPolicy](w, r)
		if created == nil {
			return
		}
		id := r.URL.Query().Get("serviceConnectionPolicyId")
		s.policies[id] = created
		s.writes = append(s.writes, "create policy "+id)
		writeBody(w, map[string]any{"name": "projects/acme-prod/locations/europe-west1/operations/op-policy", "done": true})
	case http.MethodDelete:
		if s.policies[name] == nil {
			missing(w)
			return
		}
		delete(s.policies, name)
		s.writes = append(s.writes, "delete policy "+name)
		writeBody(w, map[string]any{"name": "projects/acme-prod/locations/europe-west1/operations/op-policy", "done": true})
	}
}

func (s *networkServer) readPolicy(w http.ResponseWriter, subnet string) {
	if found := s.iam[subnet]; found != nil {
		writeBody(w, found)
		return
	}
	writeBody(w, &compute.Policy{Etag: "BwX="})
}

func (s *networkServer) writePolicy(w http.ResponseWriter, r *http.Request, subnet string) {
	sent := readBody[compute.RegionSetPolicyRequest](w, r)
	if sent == nil {
		return
	}
	s.iam[subnet] = sent.Policy
	s.writes = append(s.writes, "grant on "+subnet)
	writeBody(w, sent.Policy)
}

func (s *networkServer) wrote() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...)
}

func (s *networkServer) installed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.networks["ocel-production"] = &compute.Network{Name: "ocel-production"}
	s.subnets["ocel-production"] = &compute.Subnetwork{Name: "ocel-production", IpCidrRange: kvSubnetRange}
	s.policies["ocel-production-memorystore"] = &networkconnectivity.ServiceConnectionPolicy{ServiceClass: memorystoreServiceClass}
	s.iam["ocel-production"] = &compute.Policy{Bindings: []*compute.Binding{{Role: networkUserRole, Members: []string{serviceAgent}}}}
}
