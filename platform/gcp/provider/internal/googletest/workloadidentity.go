package googletest

import (
	"net/http"
	"strings"
	"sync"

	"google.golang.org/api/iam/v1"
)

const (
	identityPoolsPath = "/v1/projects/" + Project + "/locations/global/workloadIdentityPools"
	identityDeleted   = "DELETED"
)

type IdentityPools struct {
	Mu        sync.Mutex
	Pools     map[string]*iam.WorkloadIdentityPool
	Providers map[string]*iam.WorkloadIdentityPoolProvider
	Deleted   []string
	Patched   []string
}

func NewIdentityPools() *IdentityPools {
	return &IdentityPools{Pools: map[string]*iam.WorkloadIdentityPool{}, Providers: map[string]*iam.WorkloadIdentityPoolProvider{}}
}

func (s *IdentityPools) Provider(pool, id string) *iam.WorkloadIdentityPoolProvider {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.Providers["projects/"+Project+"/locations/global/workloadIdentityPools/"+pool+"/providers/"+id]
}

func (s *IdentityPools) Pool(id string) *iam.WorkloadIdentityPool {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.Pools["projects/"+Project+"/locations/global/workloadIdentityPools/"+id]
}

func (s *IdentityPools) Serves(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, identityPoolsPath)
}

func (s *IdentityPools) Serve(w http.ResponseWriter, r *http.Request) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimPrefix(r.URL.Path, "/v1/")
	name, verb, _ := strings.Cut(path, ":")
	providers := strings.HasSuffix(name, "/providers") || strings.Contains(name, "/providers/")
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(name, "/providers"):
		asked := ReadBody[iam.WorkloadIdentityPoolProvider](w, r)
		if asked == nil {
			return
		}
		full := name + "/" + r.URL.Query().Get("workloadIdentityPoolProviderId")
		if pool := s.Pools[strings.TrimSuffix(name, "/providers")]; pool == nil || pool.State == identityDeleted {
			WriteNotFound(w)
			return
		}
		if s.Providers[full] != nil {
			writeConflict(w)
			return
		}
		asked.Name, asked.State = full, "ACTIVE"
		s.Providers[full] = asked
		writeDoneIdentityOperation(w)
	case r.Method == http.MethodPost && verb == "":
		asked := ReadBody[iam.WorkloadIdentityPool](w, r)
		if asked == nil {
			return
		}
		full := name + "/" + r.URL.Query().Get("workloadIdentityPoolId")
		if s.Pools[full] != nil {
			writeConflict(w)
			return
		}
		asked.Name, asked.State = full, "ACTIVE"
		s.Pools[full] = asked
		writeDoneIdentityOperation(w)
	case r.Method == http.MethodPost && verb == "undelete" && providers:
		if held := s.Providers[name]; held != nil {
			held.State = "ACTIVE"
		}
		writeDoneIdentityOperation(w)
	case r.Method == http.MethodPost && verb == "undelete":
		if held := s.Pools[name]; held != nil {
			held.State = "ACTIVE"
		}
		for path, held := range s.Providers {
			if strings.HasPrefix(path, name+"/providers/") {
				held.State = "ACTIVE"
			}
		}
		writeDoneIdentityOperation(w)
	case r.Method == http.MethodGet && providers:
		held := s.Providers[name]
		if held == nil {
			WriteNotFound(w)
			return
		}
		WriteBody(w, held)
	case r.Method == http.MethodGet:
		held := s.Pools[name]
		if held == nil {
			WriteNotFound(w)
			return
		}
		WriteBody(w, held)
	case r.Method == http.MethodPatch && providers:
		asked := ReadBody[iam.WorkloadIdentityPoolProvider](w, r)
		if asked == nil {
			return
		}
		held := s.Providers[name]
		if held == nil {
			WriteNotFound(w)
			return
		}
		asked.Name, asked.State = name, held.State
		s.Providers[name] = asked
		s.Patched = append(s.Patched, name)
		writeDoneIdentityOperation(w)
	case r.Method == http.MethodDelete && providers:
		held := s.Providers[name]
		if held == nil || held.State == identityDeleted {
			WriteNotFound(w)
			return
		}
		held.State = identityDeleted
		s.Deleted = append(s.Deleted, name)
		writeDoneIdentityOperation(w)
	case r.Method == http.MethodDelete:
		held := s.Pools[name]
		if held == nil || held.State == identityDeleted {
			WriteNotFound(w)
			return
		}
		held.State = identityDeleted
		for path, provider := range s.Providers {
			if strings.HasPrefix(path, name+"/providers/") {
				provider.State = identityDeleted
			}
		}
		s.Deleted = append(s.Deleted, name)
		writeDoneIdentityOperation(w)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func writeDoneIdentityOperation(w http.ResponseWriter) {
	WriteBody(w, &iam.Operation{Name: "operations/done", Done: true})
}
