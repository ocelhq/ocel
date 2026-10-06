package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"cloud.google.com/go/iam/apiv1/iampb"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/iam/v1"
	pubsub "google.golang.org/api/pubsub/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type iamServer struct {
	kmspb.UnimplementedKeyManagementServiceServer
	iampb.UnimplementedIAMPolicyServer

	mu        sync.Mutex
	keys      map[string]bool
	keyPolicy map[string]*iampb.Policy
	keyRaces  int
	project   *cloudresourcemanager.Policy
	writes    int
	created   []*iam.CreateServiceAccountRequest

	accounts       map[string]bool
	unseen         int
	quotaFull      bool
	quotaThrottled bool

	projectWrites   int
	projectStale    int
	projectAttempts int

	accountPolicies     map[string]*iam.Policy
	accountWrites       int
	accountPolicyUnseen int
	accountPolicyDenied bool

	topicPolicies map[string]*pubsub.Policy
	topicWrites   int
	deletedTopics map[string]bool

	queuePolicies map[string]*iampb.Policy
	queueWrites   int
	queueAborts   int

	tasksAbsent bool
}

type delayQueues struct {
	cloudtaskspb.UnimplementedCloudTasksServer
	server *iamServer
}

func (q delayQueues) CreateQueue(_ context.Context, req *cloudtaskspb.CreateQueueRequest) (*cloudtaskspb.Queue, error) {
	return req.GetQueue(), nil
}

func (q delayQueues) GetIamPolicy(_ context.Context, req *iampb.GetIamPolicyRequest) (*iampb.Policy, error) {
	q.server.mu.Lock()
	defer q.server.mu.Unlock()
	if q.server.tasksAbsent {
		return nil, status.Error(codes.NotFound, req.GetResource())
	}
	if policy := q.server.queuePolicies[req.GetResource()]; policy != nil {
		return policy, nil
	}
	return &iampb.Policy{Etag: []byte("BwXhoLA=")}, nil
}

func (q delayQueues) SetIamPolicy(_ context.Context, req *iampb.SetIamPolicyRequest) (*iampb.Policy, error) {
	q.server.mu.Lock()
	defer q.server.mu.Unlock()
	if q.server.queueAborts > 0 {
		q.server.queueAborts--
		return nil, status.Error(codes.Aborted, "the queue's policy changed under this write")
	}
	if q.server.queuePolicies == nil {
		q.server.queuePolicies = map[string]*iampb.Policy{}
	}
	q.server.queuePolicies[req.GetResource()] = req.GetPolicy()
	q.server.queueWrites++
	return req.GetPolicy(), nil
}

func (s *iamServer) GetCryptoKey(_ context.Context, req *kmspb.GetCryptoKeyRequest) (*kmspb.CryptoKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.keys[req.GetName()] {
		return nil, status.Error(codes.NotFound, req.GetName())
	}
	return &kmspb.CryptoKey{Name: req.GetName()}, nil
}

func (s *iamServer) GetIamPolicy(_ context.Context, req *iampb.GetIamPolicyRequest) (*iampb.Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if policy := s.keyPolicy[req.GetResource()]; policy != nil {
		return policy, nil
	}
	return &iampb.Policy{Etag: []byte("BwXhoLA=")}, nil
}

func (s *iamServer) SetIamPolicy(_ context.Context, req *iampb.SetIamPolicyRequest) (*iampb.Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keyRaces > 0 {
		s.keyRaces--
		s.keyPolicy[req.GetResource()] = &iampb.Policy{Etag: []byte("BwXhoLB="), Bindings: []*iampb.Binding{{Role: "roles/cloudkms.viewer", Members: []string{"user:concurrent"}}}}
		return nil, status.Error(codes.FailedPrecondition, "the key's policy changed under this write")
	}
	s.keyPolicy[req.GetResource()] = req.GetPolicy()
	s.writes++
	return req.GetPolicy(), nil
}

func (s *iamServer) rest(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && path == "/v1/projects/acme-prod":
			w.Write([]byte(`{"projectId":"acme-prod","projectNumber":"123456789"}`))
		case strings.HasPrefix(path, "/v1/projects/") && strings.HasSuffix(path, ":getIamPolicy") && !strings.Contains(path, "/serviceAccounts/") && !strings.Contains(path, "/topics/"):
			_ = json.NewEncoder(w).Encode(s.project)
		case strings.HasPrefix(path, "/v1/projects/") && strings.HasSuffix(path, ":setIamPolicy") && !strings.Contains(path, "/serviceAccounts/") && !strings.Contains(path, "/topics/"):
			var asked cloudresourcemanager.SetIamPolicyRequest
			if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.projectAttempts++
			if s.projectStale > 0 {
				s.projectStale--
				w.WriteHeader(http.StatusPreconditionFailed)
				w.Write([]byte(`{"error":{"code":412,"status":"FAILED_PRECONDITION","message":"etag mismatch"}}`))
				return
			}
			if s.unseen > 0 {
				s.unseen--
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"Service account does not exist."}}`))
				return
			}
			s.project = asked.Policy
			s.writes++
			s.projectWrites++
			_ = json.NewEncoder(w).Encode(s.project)
		case strings.Contains(path, "/topics/") && s.deletedTopics[strings.TrimSuffix(strings.TrimSuffix(path, ":getIamPolicy"), ":setIamPolicy")]:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"topic not found"}}`))
		case strings.Contains(path, "/topics/") && strings.HasSuffix(path, ":getIamPolicy"):
			topic := strings.TrimSuffix(path, ":getIamPolicy")
			policy := s.topicPolicies[topic]
			if policy == nil {
				policy = &pubsub.Policy{Etag: "BwXhoLA="}
			}
			_ = json.NewEncoder(w).Encode(policy)
		case strings.Contains(path, "/topics/") && strings.HasSuffix(path, ":setIamPolicy"):
			var asked pubsub.SetIamPolicyRequest
			if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if s.topicPolicies == nil {
				s.topicPolicies = map[string]*pubsub.Policy{}
			}
			s.topicPolicies[strings.TrimSuffix(path, ":setIamPolicy")] = asked.Policy
			s.topicWrites++
			_ = json.NewEncoder(w).Encode(asked.Policy)
		case s.tasksAbsent && strings.Contains(path, "/serviceAccounts/") && strings.HasSuffix(path, ":getIamPolicy"):
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"service account not found"}}`))
		case s.accountPolicyUnseen > 0 && strings.Contains(path, "/serviceAccounts/") && strings.HasSuffix(path, ":getIamPolicy"):
			s.accountPolicyUnseen--
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"service account not found"}}`))
		case s.accountPolicyDenied && strings.Contains(path, "/serviceAccounts/") && strings.HasSuffix(path, ":setIamPolicy"):
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":{"code":403,"status":"PERMISSION_DENIED","message":"denied"}}`))
		case s.accountPolicies != nil && strings.Contains(path, "/serviceAccounts/") && strings.HasSuffix(path, ":getIamPolicy"):
			policy := s.accountPolicies[strings.TrimSuffix(path, ":getIamPolicy")]
			if policy == nil {
				policy = &iam.Policy{Etag: "BwXhoLA="}
			}
			_ = json.NewEncoder(w).Encode(policy)
		case s.accountPolicies != nil && strings.Contains(path, "/serviceAccounts/") && strings.HasSuffix(path, ":setIamPolicy"):
			var asked iam.SetIamPolicyRequest
			if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.accountPolicies[strings.TrimSuffix(path, ":setIamPolicy")] = asked.Policy
			s.accountWrites++
			_ = json.NewEncoder(w).Encode(asked.Policy)
		case strings.Contains(path, "/serviceAccounts/") && strings.HasSuffix(path, ":getIamPolicy"):
			w.Write([]byte(`{"etag":"BwXhoLA=","bindings":[{"role":"roles/iam.serviceAccountUser","members":["user:emulator"]}]}`))
		case strings.Contains(path, "/serviceAccounts/") && strings.HasSuffix(path, ":setIamPolicy"):
			w.Write([]byte(`{"etag":"BwXhoLB="}`))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/serviceAccounts"):
			var asked iam.CreateServiceAccountRequest
			if err := json.NewDecoder(r.Body).Decode(&asked); err == nil {
				s.created = append(s.created, &asked)
			}
			switch {
			case s.quotaFull:
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"Maximum number of service accounts on project acme-prod reached."}}`))
			case s.quotaThrottled:
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"quota exceeded"}}`))
			case s.accounts != nil:
				s.accounts[asked.AccountId] = true
				w.Write([]byte(`{"email":"` + asked.AccountId + `@acme-prod.iam.gserviceaccount.com"}`))
			default:
				w.WriteHeader(http.StatusConflict)
				w.Write([]byte(`{"error":{"code":409,"message":"already exists"}}`))
			}
		case r.Method == http.MethodDelete && strings.Contains(path, "/serviceAccounts/"):
			w.Write([]byte(`{}`))
		case s.accounts != nil && r.Method == http.MethodGet && strings.Contains(path, "/serviceAccounts/") &&
			!s.accounts[strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], "@acme-prod.iam.gserviceaccount.com")]:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"service account not found"}}`))
		case r.Method == http.MethodGet && strings.Contains(path, "/serviceAccounts/"):
			w.Write([]byte(`{"email":"ocel-production@acme-prod.iam.gserviceaccount.com"}`))
		default:
			t.Errorf("the bootstrap called %s %s, which nothing here serves", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func (s *iamServer) open(t *testing.T) *clients {
	t.Helper()
	return s.serve(t, s.rest(t))
}

func (s *iamServer) serve(t *testing.T, rest http.HandlerFunc) *clients {
	t.Helper()
	grpcServer := grpc.NewServer()
	kmspb.RegisterKeyManagementServiceServer(grpcServer, s)
	iampb.RegisterIAMPolicyServer(grpcServer, s)
	cloudtaskspb.RegisterCloudTasksServer(grpcServer, delayQueues{server: s})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		rest(w, r)
	}))
	server.Config.Protocols = new(http.Protocols)
	server.Config.Protocols.SetHTTP1(true)
	server.Config.Protocols.SetUnencryptedHTTP2(true)
	server.Start()
	t.Cleanup(server.Close)
	t.Cleanup(grpcServer.Stop)
	return &clients{Names: Names{namespace: "ocel", project: "acme-prod"}, region: "europe-west1", endpoint: server.URL}
}

func (s *iamServer) projectMembers(role string) ([]string, *cloudresourcemanager.Expr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, binding := range s.project.Bindings {
		if binding.Role == role {
			return binding.Members, binding.Condition
		}
	}
	return nil, nil
}

func (s *iamServer) keyMembers(key, role string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, binding := range s.keyPolicy[key].GetBindings() {
		if binding.GetRole() == role {
			return binding.GetMembers()
		}
	}
	return nil
}

func grantedIAM() *iamServer {
	return &iamServer{
		keys:      map[string]bool{"projects/acme-prod/locations/europe-west1/keyRings/ocel/cryptoKeys/production": true},
		keyPolicy: map[string]*iampb.Policy{},
		project:   &cloudresourcemanager.Policy{Etag: "BwXhoLA="},
	}
}

func TestABootstrapChecksThePermissionsItsAccountGrantsNeed(t *testing.T) {
	t.Parallel()
	for _, permission := range []string{
		"resourcemanager.projects.getIamPolicy", "resourcemanager.projects.setIamPolicy",
		"cloudkms.cryptoKeys.getIamPolicy", "cloudkms.cryptoKeys.setIamPolicy",
	} {
		if !slices.Contains(bootstrapPermissions, permission) {
			t.Errorf("a bootstrap does not check %s, and the apply would fail at an account's grant", permission)
		}
	}
	if !slices.Contains(rolesCovering(nil), "roles/resourcemanager.projectIamAdmin") {
		t.Errorf("the refusal names %v, want roles/resourcemanager.projectIamAdmin among them: it is what covers a project policy write", rolesCovering(nil))
	}
}
