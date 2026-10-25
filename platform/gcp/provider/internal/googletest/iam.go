package googletest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"cloud.google.com/go/iam/apiv1/iampb"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/iam/v1"
	pubsub "google.golang.org/api/pubsub/v1"
	storagejson "google.golang.org/api/storage/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type IAM struct {
	kmspb.UnimplementedKeyManagementServiceServer
	iampb.UnimplementedIAMPolicyServer

	Mu        sync.Mutex
	rings     map[string]bool
	keys      map[string]bool
	Destroyed map[string]bool
	Minted    []string
	KeyPolicy map[string]*iampb.Policy
	KeyRaces  int

	KeyUnserved int
	Project     *cloudresourcemanager.Policy
	Writes      int
	Created     []*iam.CreateServiceAccountRequest

	Accounts       map[string]bool
	Unseen         int
	QuotaFull      bool
	QuotaThrottled bool

	ProjectWrites   int
	ProjectStale    int
	ProjectAttempts int
	MissingRole     string
	MissingRoleAsks int

	AccountPolicies     map[string]*iam.Policy
	accountDescriptions map[string]string
	AccountWrites       int
	AccountPolicyUnseen int
	AccountPolicyDenied bool

	BucketPolicies map[string]*storagejson.Policy
	BucketWrites   int

	TopicPolicies map[string]*pubsub.Policy
	TopicWrites   int
	DeletedTopics map[string]bool

	QueuePolicies map[string]*iampb.Policy
	QueueWrites   int
	QueueAborts   int

	TasksAbsent  bool
	QueuesMade   []string
	QueuesPurged []string

	DeletedAccounts []string

	TierTags      *TierTags
	DeletedTagged []string

	ServicesOff []string
}

func UniqueIDOf(account string) string {
	sum := uint64(1469598103934665603)
	for _, b := range []byte(account) {
		sum = (sum ^ uint64(b)) * 1099511628211
	}
	return "1" + strconv.FormatUint(sum, 10)
}

func AccountTagParent(account string) string {
	return "//iam.googleapis.com/projects/" + Project + "/serviceAccounts/" + UniqueIDOf(account)
}

func accountIDIn(path string) string {
	return strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], "@acme-prod.iam.gserviceaccount.com")
}

type delayQueues struct {
	cloudtaskspb.UnimplementedCloudTasksServer
	server *IAM
}

func (q delayQueues) CreateQueue(_ context.Context, req *cloudtaskspb.CreateQueueRequest) (*cloudtaskspb.Queue, error) {
	q.server.Mu.Lock()
	defer q.server.Mu.Unlock()
	q.server.QueuesMade = append(q.server.QueuesMade, req.GetQueue().GetName())
	return req.GetQueue(), nil
}

func (q delayQueues) GetQueue(_ context.Context, req *cloudtaskspb.GetQueueRequest) (*cloudtaskspb.Queue, error) {
	return &cloudtaskspb.Queue{Name: req.GetName()}, nil
}

func (q delayQueues) PurgeQueue(_ context.Context, req *cloudtaskspb.PurgeQueueRequest) (*cloudtaskspb.Queue, error) {
	q.server.Mu.Lock()
	defer q.server.Mu.Unlock()
	q.server.QueuesPurged = append(q.server.QueuesPurged, req.GetName())
	return &cloudtaskspb.Queue{Name: req.GetName()}, nil
}

func (q delayQueues) GetIamPolicy(_ context.Context, req *iampb.GetIamPolicyRequest) (*iampb.Policy, error) {
	q.server.Mu.Lock()
	defer q.server.Mu.Unlock()
	if q.server.TasksAbsent {
		return nil, status.Error(codes.NotFound, req.GetResource())
	}
	if policy := q.server.QueuePolicies[req.GetResource()]; policy != nil {
		return policy, nil
	}
	return &iampb.Policy{Etag: []byte("BwXhoLA=")}, nil
}

func (q delayQueues) SetIamPolicy(_ context.Context, req *iampb.SetIamPolicyRequest) (*iampb.Policy, error) {
	q.server.Mu.Lock()
	defer q.server.Mu.Unlock()
	if q.server.QueueAborts > 0 {
		q.server.QueueAborts--
		return nil, status.Error(codes.Aborted, "the queue's policy changed under this write")
	}
	if q.server.QueuePolicies == nil {
		q.server.QueuePolicies = map[string]*iampb.Policy{}
	}
	q.server.QueuePolicies[req.GetResource()] = req.GetPolicy()
	q.server.QueueWrites++
	return req.GetPolicy(), nil
}

func (s *IAM) CreateKeyRing(_ context.Context, req *kmspb.CreateKeyRingRequest) (*kmspb.KeyRing, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	name := req.GetParent() + "/keyRings/" + req.GetKeyRingId()
	if s.rings == nil {
		s.rings = map[string]bool{}
	}
	if s.rings[name] {
		return nil, status.Error(codes.AlreadyExists, name)
	}
	s.rings[name] = true
	return &kmspb.KeyRing{Name: name}, nil
}

func (s *IAM) GetKeyRing(_ context.Context, req *kmspb.GetKeyRingRequest) (*kmspb.KeyRing, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if !s.rings[req.GetName()] {
		return nil, status.Error(codes.NotFound, req.GetName())
	}
	return &kmspb.KeyRing{Name: req.GetName()}, nil
}

func (s *IAM) HasKeyRing(name string) bool {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.rings[name]
}

func (s *IAM) HasKey(name string) bool {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.keys[name]
}

func (s *IAM) unservedKey() bool {
	if s.KeyUnserved == 0 {
		return false
	}
	s.KeyUnserved--
	return true
}

func (s *IAM) Encrypt(_ context.Context, req *kmspb.EncryptRequest) (*kmspb.EncryptResponse, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if !s.keys[req.GetName()] || s.unservedKey() {
		return nil, status.Error(codes.NotFound, req.GetName())
	}
	return &kmspb.EncryptResponse{Name: req.GetName(), Ciphertext: append([]byte("sealed:"), req.GetPlaintext()...)}, nil
}

func (s *IAM) CreateCryptoKey(_ context.Context, req *kmspb.CreateCryptoKeyRequest) (*kmspb.CryptoKey, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	name := req.GetParent() + "/cryptoKeys/" + req.GetCryptoKeyId()
	if s.keys == nil {
		s.keys = map[string]bool{}
	}
	if s.keys[name] {
		return nil, status.Error(codes.AlreadyExists, name)
	}
	s.keys[name] = true
	return &kmspb.CryptoKey{Name: name}, nil
}

func (s *IAM) serveBucketPolicy(w http.ResponseWriter, r *http.Request, bucket string) {
	current := s.BucketPolicies[bucket]
	if current == nil {
		current = &storagejson.Policy{Etag: "CAE=", Version: 1}
	}
	switch r.Method {
	case http.MethodGet:
		WriteBody(w, current)
	case http.MethodPut:
		var asked storagejson.Policy
		if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if s.BucketPolicies == nil {
			s.BucketPolicies = map[string]*storagejson.Policy{}
		}
		s.BucketWrites++
		asked.Etag = "CA" + strconv.Itoa(s.BucketWrites)
		s.BucketPolicies[bucket] = &asked
		WriteBody(w, &asked)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *IAM) GetCryptoKey(_ context.Context, req *kmspb.GetCryptoKeyRequest) (*kmspb.CryptoKey, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if !s.keys[req.GetName()] || s.unservedKey() {
		return nil, status.Error(codes.NotFound, req.GetName())
	}
	primary := kmspb.CryptoKeyVersion_ENABLED
	if s.Destroyed[req.GetName()] {
		primary = kmspb.CryptoKeyVersion_DESTROYED
	}
	return &kmspb.CryptoKey{Name: req.GetName(), Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT,
		Primary: &kmspb.CryptoKeyVersion{Name: req.GetName() + "/cryptoKeyVersions/1", State: primary}}, nil
}

func (s *IAM) CreateCryptoKeyVersion(_ context.Context, req *kmspb.CreateCryptoKeyVersionRequest) (*kmspb.CryptoKeyVersion, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.Minted = append(s.Minted, req.GetParent())
	return &kmspb.CryptoKeyVersion{Name: req.GetParent() + "/cryptoKeyVersions/2", State: kmspb.CryptoKeyVersion_ENABLED}, nil
}

func (s *IAM) GetCryptoKeyVersion(_ context.Context, req *kmspb.GetCryptoKeyVersionRequest) (*kmspb.CryptoKeyVersion, error) {
	return &kmspb.CryptoKeyVersion{Name: req.GetName(), State: kmspb.CryptoKeyVersion_ENABLED}, nil
}

func (s *IAM) UpdateCryptoKeyPrimaryVersion(_ context.Context, req *kmspb.UpdateCryptoKeyPrimaryVersionRequest) (*kmspb.CryptoKey, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	delete(s.Destroyed, req.GetName())
	return &kmspb.CryptoKey{Name: req.GetName()}, nil
}

func (s *IAM) GetIamPolicy(_ context.Context, req *iampb.GetIamPolicyRequest) (*iampb.Policy, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if strings.Contains(req.GetResource(), "/cryptoKeys/") && s.unservedKey() {
		return nil, status.Error(codes.NotFound, req.GetResource())
	}
	if policy := s.KeyPolicy[req.GetResource()]; policy != nil {
		return policy, nil
	}
	return &iampb.Policy{Etag: []byte("BwXhoLA=")}, nil
}

func (s *IAM) SetIamPolicy(_ context.Context, req *iampb.SetIamPolicyRequest) (*iampb.Policy, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if s.KeyRaces > 0 {
		s.KeyRaces--
		s.KeyPolicy[req.GetResource()] = &iampb.Policy{Etag: []byte("BwXhoLB="), Bindings: []*iampb.Binding{{Role: "roles/cloudkms.viewer", Members: []string{"user:concurrent"}}}}
		return nil, status.Error(codes.FailedPrecondition, "the key's policy changed under this write")
	}
	s.KeyPolicy[req.GetResource()] = req.GetPolicy()
	s.Writes++
	return req.GetPolicy(), nil
}

func (s *IAM) Rest(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.Mu.Lock()
		defer s.Mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case strings.HasPrefix(path, "/storage/v1/b/") && strings.HasSuffix(path, "/iam"):
			s.serveBucketPolicy(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/storage/v1/b/"), "/iam"))
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/projects/acme-prod/services/"):
			state := "ENABLED"
			if slices.Contains(s.ServicesOff, strings.TrimPrefix(path, "/v1/projects/acme-prod/services/")) {
				state = "DISABLED"
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"name": path, "state": state})
		case r.Method == http.MethodGet && path == "/v1/projects/acme-prod":
			_, _ = w.Write([]byte(`{"projectId":"acme-prod","projectNumber":"123456789"}`))
		case strings.HasPrefix(path, "/v1/projects/") && strings.HasSuffix(path, ":getIamPolicy") && !strings.Contains(path, "/serviceAccounts/") && !strings.Contains(path, "/topics/"):
			_ = json.NewEncoder(w).Encode(s.Project)
		case strings.HasPrefix(path, "/v1/projects/") && strings.HasSuffix(path, ":setIamPolicy") && !strings.Contains(path, "/serviceAccounts/") && !strings.Contains(path, "/topics/"):
			var asked cloudresourcemanager.SetIamPolicyRequest
			if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.ProjectAttempts++
			if s.MissingRole != "" && slices.ContainsFunc(asked.Policy.Bindings, func(b *cloudresourcemanager.Binding) bool { return b.Role == s.MissingRole }) {
				s.MissingRoleAsks++
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"Role (` + s.MissingRole + `) does not exist in the resource's hierarchy."}}`))
				return
			}
			if s.ProjectStale > 0 {
				s.ProjectStale--
				w.WriteHeader(http.StatusPreconditionFailed)
				_, _ = w.Write([]byte(`{"error":{"code":412,"status":"FAILED_PRECONDITION","message":"etag mismatch"}}`))
				return
			}
			if s.Unseen > 0 {
				s.Unseen--
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"Service account does not exist."}}`))
				return
			}
			s.Project = asked.Policy
			s.Writes++
			s.ProjectWrites++
			_ = json.NewEncoder(w).Encode(s.Project)
		case strings.Contains(path, "/topics/") && s.DeletedTopics[strings.TrimSuffix(strings.TrimSuffix(path, ":getIamPolicy"), ":setIamPolicy")]:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"topic not found"}}`))
		case strings.Contains(path, "/topics/") && strings.HasSuffix(path, ":getIamPolicy"):
			topic := strings.TrimSuffix(path, ":getIamPolicy")
			policy := s.TopicPolicies[topic]
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
			if s.TopicPolicies == nil {
				s.TopicPolicies = map[string]*pubsub.Policy{}
			}
			s.TopicPolicies[strings.TrimSuffix(path, ":setIamPolicy")] = asked.Policy
			s.TopicWrites++
			_ = json.NewEncoder(w).Encode(asked.Policy)
		case s.TasksAbsent && strings.Contains(path, "/serviceAccounts/") && strings.HasSuffix(path, ":getIamPolicy"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"service account not found"}}`))
		case s.AccountPolicyUnseen > 0 && strings.Contains(path, "/serviceAccounts/") && strings.HasSuffix(path, ":getIamPolicy"):
			s.AccountPolicyUnseen--
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"service account not found"}}`))
		case s.AccountPolicyDenied && strings.Contains(path, "/serviceAccounts/") && strings.HasSuffix(path, ":setIamPolicy"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":403,"status":"PERMISSION_DENIED","message":"denied"}}`))
		case s.AccountPolicies != nil && strings.Contains(path, "/serviceAccounts/") && strings.HasSuffix(path, ":getIamPolicy"):
			policy := s.AccountPolicies[strings.TrimSuffix(path, ":getIamPolicy")]
			if policy == nil {
				policy = &iam.Policy{Etag: "BwXhoLA="}
			}
			_ = json.NewEncoder(w).Encode(policy)
		case s.AccountPolicies != nil && strings.Contains(path, "/serviceAccounts/") && strings.HasSuffix(path, ":setIamPolicy"):
			var asked iam.SetIamPolicyRequest
			if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.AccountPolicies[strings.TrimSuffix(path, ":setIamPolicy")] = asked.Policy
			s.AccountWrites++
			_ = json.NewEncoder(w).Encode(asked.Policy)
		case strings.Contains(path, "/serviceAccounts/") && strings.HasSuffix(path, ":getIamPolicy"):
			_, _ = w.Write([]byte(`{"etag":"BwXhoLA=","bindings":[{"role":"roles/iam.serviceAccountUser","members":["user:emulator"]}]}`))
		case strings.Contains(path, "/serviceAccounts/") && strings.HasSuffix(path, ":setIamPolicy"):
			_, _ = w.Write([]byte(`{"etag":"BwXhoLB="}`))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/serviceAccounts"):
			var asked iam.CreateServiceAccountRequest
			if err := json.NewDecoder(r.Body).Decode(&asked); err == nil {
				s.Created = append(s.Created, &asked)
			}
			switch {
			case s.QuotaFull:
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"Maximum number of service accounts on project acme-prod reached."}}`))
			case s.QuotaThrottled:
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"quota exceeded"}}`))
			case s.Accounts != nil:
				s.Accounts[asked.AccountId] = true
				WriteBody(w, &iam.ServiceAccount{Email: asked.AccountId + "@acme-prod.iam.gserviceaccount.com", UniqueId: UniqueIDOf(asked.AccountId)})
			default:
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":{"code":409,"message":"already exists"}}`))
			}
		case r.Method == http.MethodDelete && strings.Contains(path, "/serviceAccounts/"):
			s.DeletedAccounts = append(s.DeletedAccounts, path)
			if s.TierTags != nil && len(s.TierTags.Bound(AccountTagParent(accountIDIn(path)))) > 0 {
				s.DeletedTagged = append(s.DeletedTagged, path)
			}
			if s.Accounts != nil {
				if !s.Accounts[accountIDIn(path)] {
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"service account not found"}}`))
					return
				}
				delete(s.Accounts, accountIDIn(path))
			}
			_, _ = w.Write([]byte(`{}`))
		case s.Accounts != nil && r.Method == http.MethodGet && strings.Contains(path, "/serviceAccounts/") &&
			!s.Accounts[strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], "@acme-prod.iam.gserviceaccount.com")]:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"service account not found"}}`))
		case r.Method == http.MethodGet && s.accountDescriptions[path[strings.LastIndex(path, "/")+1:]] != "":
			id := strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], "@acme-prod.iam.gserviceaccount.com")
			_ = json.NewEncoder(w).Encode(&iam.ServiceAccount{Email: id + "@acme-prod.iam.gserviceaccount.com", UniqueId: UniqueIDOf(id), Description: s.accountDescriptions[path[strings.LastIndex(path, "/")+1:]]})
		case r.Method == http.MethodGet && strings.Contains(path, "/serviceAccounts/"):
			WriteBody(w, &iam.ServiceAccount{Email: accountIDIn(path) + "@acme-prod.iam.gserviceaccount.com", UniqueId: UniqueIDOf(accountIDIn(path))})
		default:
			t.Errorf("the bootstrap called %s %s, which nothing here serves", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func (s *IAM) Open(t *testing.T) *gcloud.Clients {
	t.Helper()
	return s.Serve(t, s.Rest(t))
}

func (s *IAM) Serve(t *testing.T, rest http.HandlerFunc) *gcloud.Clients {
	t.Helper()
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(s.refuseCallsToServicesOff))
	kmspb.RegisterKeyManagementServiceServer(grpcServer, s)
	iampb.RegisterIAMPolicyServer(grpcServer, s)
	cloudtaskspb.RegisterCloudTasksServer(grpcServer, delayQueues{server: s})
	firestorepb.RegisterFirestoreServer(grpcServer, emptyFirestore{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		if api := restAPI(r.URL.Path); slices.Contains(s.ServicesOff, api) {
			writeServiceDisabled(w, api)
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
	return WithTierBuckets(&gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1", Endpoint: server.URL})
}

func restAPI(path string) string {
	switch {
	case strings.HasPrefix(path, "/v2/"):
		return "run.googleapis.com"
	case strings.HasPrefix(path, "/v1/") && strings.Contains(path, "/jobs"):
		return "cloudscheduler.googleapis.com"
	}
	return ""
}

func grpcAPI(method string, req any) string {
	asked, policy := req.(interface{ GetResource() string })
	if strings.HasPrefix(method, "/google.cloud.kms.v1.") || policy && strings.Contains(asked.GetResource(), "/keyRings/") {
		return "cloudkms.googleapis.com"
	}
	return ""
}

func serviceDisabled(api string) string {
	return api + " has not been used in project " + Project + " before or it is disabled"
}

func writeServiceDisabled(w http.ResponseWriter, api string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":{"code":403,"status":"PERMISSION_DENIED","message":"` + serviceDisabled(api) + `.",` +
		`"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"SERVICE_DISABLED"}]}}`))
}

func (s *IAM) refuseCallsToServicesOff(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if api := grpcAPI(info.FullMethod, req); api != "" && slices.Contains(s.ServicesOff, api) {
		return nil, status.Error(codes.PermissionDenied, serviceDisabled(api))
	}
	return handler(ctx, req)
}

func (s *IAM) ProjectMembers(role string) ([]string, *cloudresourcemanager.Expr) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	for _, binding := range s.Project.Bindings {
		if binding.Role == role {
			return binding.Members, binding.Condition
		}
	}
	return nil, nil
}

func (s *IAM) KeyMembers(key, role string) []string {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	for _, binding := range s.KeyPolicy[key].GetBindings() {
		if binding.GetRole() == role {
			return binding.GetMembers()
		}
	}
	return nil
}

func GrantedIAM() *IAM {
	return &IAM{
		rings:     map[string]bool{"projects/acme-prod/locations/europe-west1/keyRings/ocel": true},
		keys:      map[string]bool{"projects/acme-prod/locations/europe-west1/keyRings/ocel/cryptoKeys/production": true},
		KeyPolicy: map[string]*iampb.Policy{},
		Project:   &cloudresourcemanager.Policy{Etag: "BwXhoLA="},
	}
}

type emptyFirestore struct {
	firestorepb.UnimplementedFirestoreServer
}

func (emptyFirestore) BatchGetDocuments(*firestorepb.BatchGetDocumentsRequest, firestorepb.Firestore_BatchGetDocumentsServer) error {
	return status.Error(codes.NotFound, "no such document")
}
