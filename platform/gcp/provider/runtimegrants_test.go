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

	"cloud.google.com/go/iam/apiv1/iampb"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type iamServer struct {
	kmspb.UnimplementedKeyManagementServiceServer
	iampb.UnimplementedIAMPolicyServer

	mu        sync.Mutex
	keys      map[string]bool
	keyPolicy map[string]*iampb.Policy
	project   *cloudresourcemanager.Policy
	writes    int
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
	if held := s.keyPolicy[req.GetResource()]; held != nil {
		return held, nil
	}
	return &iampb.Policy{Etag: []byte("BwXhoLA=")}, nil
}

func (s *iamServer) SetIamPolicy(_ context.Context, req *iampb.SetIamPolicyRequest) (*iampb.Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		case strings.HasPrefix(path, "/v1/projects/") && strings.HasSuffix(path, ":getIamPolicy") && !strings.Contains(path, "/serviceAccounts/"):
			_ = json.NewEncoder(w).Encode(s.project)
		case strings.HasPrefix(path, "/v1/projects/") && strings.HasSuffix(path, ":setIamPolicy") && !strings.Contains(path, "/serviceAccounts/"):
			var asked cloudresourcemanager.SetIamPolicyRequest
			if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.project = asked.Policy
			s.writes++
			_ = json.NewEncoder(w).Encode(s.project)
		case strings.Contains(path, "/serviceAccounts/") && strings.HasSuffix(path, ":getIamPolicy"):
			w.Write([]byte(`{"etag":"BwXhoLA=","bindings":[{"role":"roles/iam.serviceAccountUser","members":["user:emulator"]}]}`))
		case strings.Contains(path, "/serviceAccounts/") && strings.HasSuffix(path, ":setIamPolicy"):
			w.Write([]byte(`{"etag":"BwXhoLB="}`))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/serviceAccounts"):
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":{"code":409,"message":"already exists"}}`))
		case r.Method == http.MethodDelete && strings.Contains(path, "/serviceAccounts/"):
			w.Write([]byte(`{}`))
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
	grpcServer := grpc.NewServer()
	kmspb.RegisterKeyManagementServiceServer(grpcServer, s)
	iampb.RegisterIAMPolicyServer(grpcServer, s)
	rest := s.rest(t)
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

func standingIAM() *iamServer {
	return &iamServer{
		keys:      map[string]bool{"projects/acme-prod/locations/europe-west1/keyRings/ocel/cryptoKeys/production": true},
		keyPolicy: map[string]*iampb.Policy{},
		project:   &cloudresourcemanager.Policy{Etag: "BwXhoLA="},
	}
}

func TestTheRuntimeAccountIsHeldToReadingThisDatabaseAndOpeningUnderTheClassKeyAlone(t *testing.T) {
	t.Parallel()
	server := standingIAM()
	b := bootstrap{clients: server.open(t)}
	read := survey{Class: providerkit.ClassProduction, Names: b.clients.Names}
	ctx := context.Background()

	if err := b.makeAccount(ctx, read, "ocel-production"); err != nil {
		t.Fatalf("makeAccount() = %v", err)
	}
	const member = "serviceAccount:ocel-production@acme-prod.iam.gserviceaccount.com"
	members, condition := server.projectMembers(runtimeRecordsRole)
	if !slices.Contains(members, member) {
		t.Errorf("the project binds %v to %s, want the runtime account: a container's runtime reads its records with it", members, runtimeRecordsRole)
	}
	if condition == nil || !strings.Contains(condition.Expression, "projects/acme-prod/databases/ocel") {
		t.Errorf("the read is conditioned on %+v, want this namespace's one database: IAM fences Firestore no finer than a database", condition)
	}
	if held, _ := server.projectMembers("roles/datastore.user"); len(held) > 0 {
		t.Errorf("the runtime account holds roles/datastore.user, and a runtime writes nothing")
	}
	key := "projects/acme-prod/locations/europe-west1/keyRings/ocel/cryptoKeys/production"
	if held := server.keyMembers(key, runtimeOpeningRole); !slices.Contains(held, member) {
		t.Errorf("the production key binds %v to %s, want the runtime account: it opens what the deploy sealed", held, runtimeOpeningRole)
	}
	if held := server.keyMembers(key, connectorSealingRole); len(held) > 0 {
		t.Errorf("the runtime account holds %s, and a runtime seals nothing", connectorSealingRole)
	}

	stands, err := b.accountStands(ctx, providerkit.ClassProduction, "ocel-production")
	if err != nil {
		t.Fatalf("accountStands() = %v", err)
	}
	if !stands.held || stands.mends != "" {
		t.Errorf("accountStands() = %+v after the grants landed, want it standing with nothing to mend", stands)
	}
	written := server.writes
	if err := b.makeAccount(ctx, read, "ocel-production"); err != nil {
		t.Fatalf("makeAccount() again = %v", err)
	}
	if server.writes != written {
		t.Errorf("a second bootstrap wrote %d more policies, want none: the grants already stand", server.writes-written)
	}
}

func TestAnAccountThatMayNotReadIsSurveyedAsMendable(t *testing.T) {
	t.Parallel()
	server := standingIAM()
	b := bootstrap{clients: server.open(t)}

	stands, err := b.accountStands(context.Background(), providerkit.ClassProduction, "ocel-production")
	if err != nil {
		t.Fatalf("accountStands() = %v", err)
	}
	if !stands.held || stands.mends != reasonUnread {
		t.Errorf("accountStands() = %+v, want it standing and mended for the read it lacks: a bootstrap made before the runtime read live would otherwise never grant it", stands)
	}
}

func TestRemovingTheAccountTakesItsReadsOffTheProjectAndTheKeyFirst(t *testing.T) {
	t.Parallel()
	server := standingIAM()
	b := bootstrap{clients: server.open(t)}
	read := survey{Class: providerkit.ClassProduction, Names: b.clients.Names}
	ctx := context.Background()
	if err := b.makeAccount(ctx, read, "ocel-production"); err != nil {
		t.Fatal(err)
	}

	if err := b.takeAccount(ctx, providerkit.ClassProduction, "ocel-production"); err != nil {
		t.Fatalf("takeAccount() = %v", err)
	}
	const member = "serviceAccount:ocel-production@acme-prod.iam.gserviceaccount.com"
	if held, _ := server.projectMembers(runtimeRecordsRole); slices.Contains(held, member) {
		t.Errorf("the project still binds the deleted account to %s, and a deleted principal's binding lingers in the policy for anyone to read", runtimeRecordsRole)
	}
	key := "projects/acme-prod/locations/europe-west1/keyRings/ocel/cryptoKeys/production"
	if held := server.keyMembers(key, runtimeOpeningRole); slices.Contains(held, member) {
		t.Errorf("the key still binds the deleted account to %s", runtimeOpeningRole)
	}
}

func TestABootstrapChecksThePermissionsTheRuntimeGrantsNeed(t *testing.T) {
	t.Parallel()
	for _, permission := range []string{
		"resourcemanager.projects.getIamPolicy", "resourcemanager.projects.setIamPolicy",
		"cloudkms.cryptoKeys.getIamPolicy", "cloudkms.cryptoKeys.setIamPolicy",
	} {
		if !slices.Contains(bootstrapPermissions, permission) {
			t.Errorf("a bootstrap does not check %s, and the apply would fail at the runtime account's grant", permission)
		}
	}
	if !slices.Contains(rolesCovering(nil), "roles/resourcemanager.projectIamAdmin") {
		t.Errorf("the refusal names %v, want roles/resourcemanager.projectIamAdmin among them: it is what covers a project policy write", rolesCovering(nil))
	}
}
