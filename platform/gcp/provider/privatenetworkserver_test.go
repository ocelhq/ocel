package gcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
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
	unnamed  bool

	databases *googletest.CloudSQL
}

func servingNetworksAndStores(t *testing.T) (bootstrap.Engine, *networkServer, *googletest.Memorystore) {
	t.Helper()
	b, networks := servingNetworks(t)
	stores, url := googletest.StartMemorystore(t)
	b.Stores = gcloud.Memorystore{Base: url, Client: http.DefaultClient}
	return b, networks, stores
}

func servingNetworks(t *testing.T) (bootstrap.Engine, *networkServer) {
	t.Helper()
	server := &networkServer{
		networks: map[string]*compute.Network{},
		subnets:  map[string]*compute.Subnetwork{},
		policies: map[string]*networkconnectivity.ServiceConnectionPolicy{},
		iam:      map[string]*compute.Policy{},

		databases: googletest.NewCloudSQL(),
	}
	served := httptest.NewServer(server.serve(t))
	t.Cleanup(served.Close)
	names := names.New("ocel", "acme-prod")
	registry := &frontRegistry{front: &countingFront{}}
	return composed(bootstrap.Engine{Clients: &gcloud.Clients{Names: names, Region: "europe-west1", Endpoint: served.URL}, Edges: registry}), server
}

func deployingNetworks(t *testing.T) (*Provider, *networkServer) {
	t.Helper()
	b, server := servingNetworks(t)
	return pushing(t, b.Clients.Endpoint), server
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
		case r.Method == http.MethodGet && strings.Contains(at, "/projects/acme-prod/services/"):
			googletest.WriteBody(w, map[string]any{"name": at, "state": bootstrap.EnabledService})
		case strings.Contains(at, "/instances"):
			s.databases.Serve(w, r)
		case strings.Contains(at, "/operations/"):
			googletest.WriteBody(w, map[string]any{"name": at, "status": gcloud.OperationDone, "done": true})
		case r.Method == http.MethodGet && strings.HasSuffix(at, "/v1/projects/acme-prod"):
			googletest.WriteBody(w, map[string]any{"projectId": "acme-prod", "projectNumber": "123456789"})
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
			googletest.WriteBody(w, found)
			return
		}
		googletest.WriteNotFound(w)
	case http.MethodPost:
		said := googletest.ReadBody[map[string]any](w, r)
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
		googletest.WriteBody(w, &compute.Operation{Name: "op-network", Status: gcloud.OperationDone})
	case http.MethodDelete:
		if s.networks[name] == nil {
			googletest.WriteNotFound(w)
			return
		}
		delete(s.networks, name)
		s.writes = append(s.writes, "delete network "+name)
		googletest.WriteBody(w, &compute.Operation{Name: "op-network", Status: gcloud.OperationDone})
	}
}

func (s *networkServer) subnet(w http.ResponseWriter, r *http.Request, name string) {
	switch r.Method {
	case http.MethodGet:
		if found := s.subnets[name]; found != nil {
			googletest.WriteBody(w, found)
			return
		}
		googletest.WriteNotFound(w)
	case http.MethodPost:
		created := googletest.ReadBody[compute.Subnetwork](w, r)
		if created == nil {
			return
		}
		s.subnets[created.Name] = created
		s.writes = append(s.writes, "create subnetwork "+created.Name)
		googletest.WriteBody(w, &compute.Operation{Name: "op-subnet", Status: gcloud.OperationDone})
	case http.MethodDelete:
		if s.subnets[name] == nil {
			googletest.WriteNotFound(w)
			return
		}
		delete(s.subnets, name)
		s.writes = append(s.writes, "delete subnetwork "+name)
		googletest.WriteBody(w, &compute.Operation{Name: "op-subnet", Status: gcloud.OperationDone})
	}
}

func (s *networkServer) policy(w http.ResponseWriter, r *http.Request, name string) {
	switch r.Method {
	case http.MethodGet:
		if found := s.policies[name]; found != nil {
			googletest.WriteBody(w, found)
			return
		}
		googletest.WriteNotFound(w)
	case http.MethodPost:
		created := googletest.ReadBody[networkconnectivity.ServiceConnectionPolicy](w, r)
		if created == nil {
			return
		}
		id := r.URL.Query().Get("serviceConnectionPolicyId")
		s.policies[id] = created
		s.writes = append(s.writes, "create policy "+id)
		if s.unnamed {
			googletest.WriteBody(w, map[string]any{"done": false})
			return
		}
		googletest.WriteBody(w, map[string]any{"name": "projects/acme-prod/locations/europe-west1/operations/op-policy", "done": true})
	case http.MethodDelete:
		if s.policies[name] == nil {
			googletest.WriteNotFound(w)
			return
		}
		delete(s.policies, name)
		s.writes = append(s.writes, "delete policy "+name)
		googletest.WriteBody(w, map[string]any{"name": "projects/acme-prod/locations/europe-west1/operations/op-policy", "done": true})
	}
}

func (s *networkServer) readPolicy(w http.ResponseWriter, subnet string) {
	if found := s.iam[subnet]; found != nil {
		googletest.WriteBody(w, found)
		return
	}
	googletest.WriteBody(w, &compute.Policy{Etag: "BwX="})
}

func (s *networkServer) writePolicy(w http.ResponseWriter, r *http.Request, subnet string) {
	sent := googletest.ReadBody[compute.RegionSetPolicyRequest](w, r)
	if sent == nil {
		return
	}
	s.iam[subnet] = sent.Policy
	s.writes = append(s.writes, "grant on "+subnet)
	googletest.WriteBody(w, sent.Policy)
}

func (s *networkServer) wrote() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...)
}

func (s *networkServer) installed() { s.installedWithout("") }

func (s *networkServer) installedWithout(policy string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.networks["ocel-prod-network"] = &compute.Network{Name: "ocel-prod-network"}
	s.subnets["ocel-prod-network"] = &compute.Subnetwork{Name: "ocel-prod-network", IpCidrRange: network.SubnetRange}
	s.policies["ocel-prod-network-memorystore"] = &networkconnectivity.ServiceConnectionPolicy{ServiceClass: network.MemorystoreServiceClass}
	s.policies["ocel-prod-network-cloudsql"] = &networkconnectivity.ServiceConnectionPolicy{ServiceClass: network.CloudSQLServiceClass}
	delete(s.policies, policy)
	s.iam["ocel-prod-network"] = &compute.Policy{Bindings: []*compute.Binding{{Role: network.NetworkUserRole, Members: []string{serviceAgent}}}}
}
