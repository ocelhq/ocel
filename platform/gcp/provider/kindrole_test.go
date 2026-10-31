package gcp

import (
	"cmp"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/iam/v1"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/control"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

const partsRoleEndpoint = "/v1/projects/acme-prod/roles/ocel_parts"

type roleServer struct {
	endpoint     string
	mu           sync.Mutex
	role         *iam.Role
	createAnswer int
	undeleteCode int
	writes       []string
	created      *iam.CreateRoleRequest
	patched      *iam.Role
	patchedMask  string
}

func (s *roleServer) handler(t *testing.T) http.HandlerFunc {
	endpoint := cmp.Or(s.endpoint, partsRoleEndpoint)
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == endpoint:
			if s.role == nil {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"role not found"}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(s.role)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/acme-prod/roles":
			s.writes = append(s.writes, "create")
			var asked iam.CreateRoleRequest
			if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.created = &asked
			if s.createAnswer != 0 {
				w.WriteHeader(s.createAnswer)
				w.Write([]byte(`{"error":{"code":409,"status":"ALREADY_EXISTS","message":"role exists"}}`))
				return
			}
			s.role = asked.Role
			s.role.Name = "projects/acme-prod/roles/" + asked.RoleId
			s.role.Etag = "BwXhoLA="
			_ = json.NewEncoder(w).Encode(s.role)
		case r.Method == http.MethodPost && r.URL.Path == endpoint+":undelete":
			s.writes = append(s.writes, "undelete")
			if s.undeleteCode != 0 {
				w.WriteHeader(s.undeleteCode)
				w.Write([]byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"role purged"}}`))
				return
			}
			s.role.Deleted = false
			_ = json.NewEncoder(w).Encode(s.role)
		case r.Method == http.MethodPatch && r.URL.Path == endpoint:
			s.writes = append(s.writes, "patch")
			var asked iam.Role
			if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.patched, s.patchedMask = &asked, r.URL.Query().Get("updateMask")
			s.role.IncludedPermissions, s.role.Stage = asked.IncludedPermissions, asked.Stage
			_ = json.NewEncoder(w).Encode(s.role)
		case r.Method == http.MethodDelete && r.URL.Path == endpoint:
			s.writes = append(s.writes, "delete")
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/secrets/"):
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":404,"message":"secret not found"}}`))
		default:
			t.Errorf("the bootstrap called %s %s, which nothing here serves", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func (s *roleServer) bootstrap(t *testing.T) bootstrap.Engine {
	t.Helper()
	return composed(bootstrap.Engine{Clients: (&googletest.IAM{}).Serve(t, s.handler(t))})
}

func TestRemovingABootstrapKeepsItsCustomRoles(t *testing.T) {
	t.Parallel()

	names := names.New("ocel", "acme-prod")
	target := bootstrap.Resource{Kind: bootstrap.KindRole, Name: names.PartsRole(), Shared: true}
	for _, sibling := range []bool{false, true} {
		read := bootstrap.Survey{Tier: environment.TierProduction, Names: names, Sibling: sibling, Held: map[string]bool{target.ID(): true}}

		taking := bootstrap.Removing(read, target)

		if taking.Action != provider.ActionKeep || taking.Reason != bootstrap.ReasonRoleKept {
			t.Errorf("removing the role with sibling=%t = %s %q, want keep %q", sibling, taking.Action, taking.Reason, bootstrap.ReasonRoleKept)
		}
	}
}

func TestAnEmulatedBootstrapMakesNoCustomRole(t *testing.T) {
	t.Parallel()

	names := names.New("ocel", "acme-prod")
	count := func(emulated bool) int {
		return len(slices.DeleteFunc(bootstrap.Resources(names, environment.TierProduction, emulated), func(each bootstrap.Resource) bool { return each.Kind != bootstrap.KindRole }))
	}
	if got := count(true); got != 0 {
		t.Errorf("an emulated bootstrap names %d custom roles, want none: floci answers 404 to every roles call", got)
	}
	if got := count(false); got != 2 {
		t.Errorf("a bootstrap against Google names %d custom roles, want the CDN purge and parts roles", got)
	}
}

func TestABootstrapCredentialMayKeepTheCustomRoleItMakes(t *testing.T) {
	t.Parallel()

	document, err := control.Credentials{Project: control.Named("acme-prod"), Namespace: "ocel"}.Permissions(edge.PurposeBootstrap, environment.TierProduction)
	if err != nil {
		t.Fatalf("Permissions(bootstrap) = %v", err)
	}
	lines := strings.Split(document.Document, "\n")
	if !slices.Contains(lines, "roles/iam.roleAdmin") {
		t.Errorf("Permissions(bootstrap) =\n%s\nwant roles/iam.roleAdmin: it is what lets a bootstrap make the role", document.Document)
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "projects/acme-prod/roles/") {
			t.Errorf("Permissions(bootstrap) names %s: the bootstrap makes that role and the role does not exist before it", line)
		}
	}
	for _, want := range []string{"iam.roles.get", "iam.roles.create", "iam.roles.update", "iam.roles.undelete"} {
		if !slices.Contains(bootstrap.PermissionsFor(nil), want) {
			t.Errorf("a bootstrap does not check %s, and the apply would fail at the role", want)
		}
	}
}
