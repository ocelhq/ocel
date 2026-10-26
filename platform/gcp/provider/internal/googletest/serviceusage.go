package googletest

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"google.golang.org/api/googleapi"
	"google.golang.org/api/serviceusage/v1"
)

const servicesPrefix = "/v1/projects/" + Project + "/services"

type ServiceUsage struct {
	mu sync.Mutex

	ServicesOff    []string
	Unbilled       bool
	UnbilledAtOnce bool
	Precondition   string
	Pending        int
	Propagating    int

	BatchGets   int
	Gets        int
	Asked       [][]string
	Enabled     [][]string
	pendingPoll int
	operations  int
}

func (s *ServiceUsage) IsOff(api string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Contains(s.ServicesOff, api)
}

func (s *ServiceUsage) EnabledIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var enabled []string
	for _, batch := range s.Enabled {
		enabled = append(enabled, batch...)
	}
	return enabled
}

func (s *ServiceUsage) Serve(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == servicesPrefix+":batchGet":
		s.batchGet(w, r)
	case r.Method == http.MethodPost && path == servicesPrefix+":batchEnable":
		s.batchEnable(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/operations/"):
		s.poll(w)
	case r.Method == http.MethodGet && strings.HasPrefix(path, servicesPrefix+"/"):
		s.get(w, strings.TrimPrefix(path, servicesPrefix+"/"))
	default:
		return false
	}
	return true
}

func (s *ServiceUsage) state(api string) string {
	if slices.Contains(s.ServicesOff, api) {
		return "DISABLED"
	}
	return "ENABLED"
}

func (s *ServiceUsage) described(api string) *serviceusage.GoogleApiServiceusageV1Service {
	return &serviceusage.GoogleApiServiceusageV1Service{
		Name:   "projects/" + ProjectNumber + "/services/" + api,
		Parent: "projects/" + ProjectNumber,
		State:  s.state(api),
		Config: &serviceusage.GoogleApiServiceusageV1ServiceConfig{Name: api},
	}
}

func (s *ServiceUsage) get(w http.ResponseWriter, api string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Gets++
	w.Header().Set("Content-Type", "application/json")
	WriteBody(w, s.described(api))
}

func (s *ServiceUsage) batchGet(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.BatchGets++
	w.Header().Set("Content-Type", "application/json")
	names := r.URL.Query()["names"]
	if len(names) > 30 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"A maximum of 30 services can be retrieved in a batch."}}`))
		return
	}
	var asked []string
	answer := &serviceusage.BatchGetServicesResponse{}
	for _, name := range names {
		api := name[strings.LastIndex(name, "/")+1:]
		asked = append(asked, api)
		described := s.described(api)
		if s.Propagating > 0 && slices.ContainsFunc(s.Enabled, func(batch []string) bool { return slices.Contains(batch, api) }) {
			described.State = "DISABLED"
		}
		answer.Services = append(answer.Services, described)
	}
	if s.Propagating > 0 && len(s.Enabled) > 0 {
		s.Propagating--
	}
	s.Asked = append(s.Asked, asked)
	WriteBody(w, answer)
}

func (s *ServiceUsage) batchEnable(w http.ResponseWriter, r *http.Request) {
	asked := ReadBody[serviceusage.BatchEnableServicesRequest](w, r)
	if asked == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if len(asked.ServiceIds) > 20 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"A maximum of 20 services can be enabled in a batch."}}`))
		return
	}
	if s.UnbilledAtOnce {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"` + unbilled(asked.ServiceIds) + `","details":[` + billingNotFound + `]}}`))
		return
	}
	if s.Precondition != "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"` + s.Precondition + `"}}`))
		return
	}
	s.operations++
	name := "operations/acf." + strconv.Itoa(s.operations)
	if s.Unbilled {
		WriteBody(w, &serviceusage.Operation{Name: name, Done: true, Error: &serviceusage.Status{
			Code:    9,
			Message: unbilled(asked.ServiceIds),
			Details: []googleapi.RawMessage{googleapi.RawMessage(billingNotFound)},
		}})
		return
	}
	s.Enabled = append(s.Enabled, asked.ServiceIds)
	s.ServicesOff = slices.DeleteFunc(s.ServicesOff, func(api string) bool { return slices.Contains(asked.ServiceIds, api) })
	s.pendingPoll = s.Pending
	WriteBody(w, &serviceusage.Operation{Name: name, Done: s.Pending == 0})
}

const billingNotFound = `{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"UREQ_PROJECT_BILLING_NOT_FOUND","domain":"serviceusage.googleapis.com"}`

func unbilled(ids []string) string {
	return "Billing account for project '" + ProjectNumber + "' is not found. Billing must be enabled for activation of service(s) '" +
		strings.Join(ids, ",") + "' to proceed."
}

func (s *ServiceUsage) poll(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if s.pendingPoll > 0 {
		s.pendingPoll--
	}
	WriteBody(w, &serviceusage.Operation{Name: "operations/acf." + strconv.Itoa(s.operations), Done: s.pendingPoll == 0})
}
