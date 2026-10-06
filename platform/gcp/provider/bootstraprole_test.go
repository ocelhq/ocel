package gcp

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/iam/v1"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const appAccountsRoleEndpoint = "/v1/projects/acme-prod/roles/ocel_app_accounts"

type roleServer struct {
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
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == appAccountsRoleEndpoint:
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
		case r.Method == http.MethodPost && r.URL.Path == appAccountsRoleEndpoint+":undelete":
			s.writes = append(s.writes, "undelete")
			if s.undeleteCode != 0 {
				w.WriteHeader(s.undeleteCode)
				w.Write([]byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"role purged"}}`))
				return
			}
			s.role.Deleted = false
			_ = json.NewEncoder(w).Encode(s.role)
		case r.Method == http.MethodPatch && r.URL.Path == appAccountsRoleEndpoint:
			s.writes = append(s.writes, "patch")
			var asked iam.Role
			if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.patched, s.patchedMask = &asked, r.URL.Query().Get("updateMask")
			s.role.IncludedPermissions, s.role.Stage = asked.IncludedPermissions, asked.Stage
			_ = json.NewEncoder(w).Encode(s.role)
		default:
			t.Errorf("the bootstrap called %s %s, which nothing here serves", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func (s *roleServer) bootstrap(t *testing.T) bootstrap {
	t.Helper()
	return bootstrap{clients: (&iamServer{}).serve(t, s.handler(t))}
}

func currentRole() *iam.Role {
	return &iam.Role{
		Name:                "projects/acme-prod/roles/ocel_app_accounts",
		IncludedPermissions: slices.Clone(appAccountsPermissions),
		Stage:               "GA",
		Etag:                "BwXhoLA=",
	}
}

func TestTheAppAccountsRoleHoldsOnlyWhatADeployDoesToAnAppsAccount(t *testing.T) {
	t.Parallel()

	want := []string{
		"iam.serviceAccounts.create",
		"iam.serviceAccounts.get",
		"iam.serviceAccounts.getIamPolicy",
		"iam.serviceAccounts.setIamPolicy",
	}
	if !slices.Equal(appAccountsPermissions, want) {
		t.Errorf("appAccountsPermissions = %v, want %v: a delete, disable, update or key permission lets a stolen deploy credential take accounts down", appAccountsPermissions, want)
	}
}

func TestABootstrapCreatesTheAppAccountsRoleOnceAndReusesIt(t *testing.T) {
	t.Parallel()
	server := &roleServer{}
	b := server.bootstrap(t)
	ctx := t.Context()
	target := item{Kind: KindRole, Name: "ocel_app_accounts"}

	found, err := b.presenceOf(ctx, environment.TierProduction, target)
	if err != nil || found.present {
		t.Fatalf("presenceOf() = %+v, %v, want an absent role", found, err)
	}
	if err := b.makeRole(ctx, target.Name); err != nil {
		t.Fatalf("makeRole() = %v", err)
	}
	if !slices.Equal(server.writes, []string{"create"}) {
		t.Fatalf("the first run wrote %v, want one create", server.writes)
	}
	asked := server.created
	if asked.RoleId != "ocel_app_accounts" || asked.Role.Stage != "GA" || !slices.Equal(asked.Role.IncludedPermissions, appAccountsPermissions) {
		t.Errorf("the role was created as %+v %+v, want id ocel_app_accounts, stage GA and the four permissions", asked, asked.Role)
	}
	if asked.Role.Title != "ocel app accounts (ocel)" {
		t.Errorf("the role is titled %q, want %q", asked.Role.Title, "ocel app accounts (ocel)")
	}

	found, err = b.presenceOf(ctx, environment.TierProduction, target)
	if err != nil || !found.present || found.mends != "" {
		t.Fatalf("presenceOf() = %+v, %v, want a present role with nothing to mend", found, err)
	}
	if err := b.makeRole(ctx, target.Name); err != nil {
		t.Fatalf("makeRole() again = %v", err)
	}
	if !slices.Equal(server.writes, []string{"create"}) {
		t.Errorf("the second run wrote %v, want no write beyond the first create", server.writes)
	}
}

func TestADeletedAppAccountsRoleIsUndeleted(t *testing.T) {
	t.Parallel()
	server := &roleServer{role: currentRole()}
	server.role.Deleted = true
	b := server.bootstrap(t)
	ctx := t.Context()
	target := item{Kind: KindRole, Name: "ocel_app_accounts"}

	found, err := b.presenceOf(ctx, environment.TierProduction, target)
	if err != nil || !found.present || found.mends != reasonRoleDeleted {
		t.Fatalf("presenceOf() = %+v, %v, want a present role to mend for %q", found, err, reasonRoleDeleted)
	}
	if err := b.makeRole(ctx, target.Name); err != nil {
		t.Fatalf("makeRole() = %v", err)
	}
	if !slices.Equal(server.writes, []string{"undelete"}) {
		t.Errorf("makeRole() wrote %v, want one undelete", server.writes)
	}
}

func TestAnAppAccountsRoleWithOtherPermissionsIsPutBack(t *testing.T) {
	t.Parallel()
	for name, drift := range map[string]func(*iam.Role){
		"an extra permission": func(role *iam.Role) {
			role.IncludedPermissions = append(role.IncludedPermissions, "iam.serviceAccounts.delete")
		},
		"a missing permission": func(role *iam.Role) { role.IncludedPermissions = role.IncludedPermissions[:3] },
		"a disabled stage":     func(role *iam.Role) { role.Stage = "DISABLED" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := &roleServer{role: currentRole()}
			drift(server.role)
			b := server.bootstrap(t)
			target := item{Kind: KindRole, Name: "ocel_app_accounts"}

			found, err := b.presenceOf(t.Context(), environment.TierProduction, target)
			if err != nil || !found.present || found.mends != reasonRoleDrifted {
				t.Fatalf("presenceOf() = %+v, %v, want a present role to mend for %q", found, err, reasonRoleDrifted)
			}
			if err := b.makeRole(t.Context(), target.Name); err != nil {
				t.Fatalf("makeRole() = %v", err)
			}
			if !slices.Equal(server.writes, []string{"patch"}) {
				t.Fatalf("makeRole() wrote %v, want one patch", server.writes)
			}
			if server.patchedMask != "includedPermissions,stage" {
				t.Errorf("the patch masked %q, want %q", server.patchedMask, "includedPermissions,stage")
			}
			if !slices.Equal(server.patched.IncludedPermissions, appAccountsPermissions) || server.patched.Stage != "GA" || server.patched.Etag != "BwXhoLA=" {
				t.Errorf("the patch sent %+v, want the four permissions, stage GA and the etag the get returned", server.patched)
			}
		})
	}
}

func TestAnAppAccountsRoleDeletedTooLongAgoIsRefusedNamingTheWait(t *testing.T) {
	t.Parallel()
	deleted := currentRole()
	deleted.Deleted = true
	for name, server := range map[string]*roleServer{
		"the undelete is refused":   {role: deleted, undeleteCode: http.StatusBadRequest},
		"the id is held by a purge": {createAnswer: http.StatusConflict},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := server.bootstrap(t)

			err := b.makeRole(t.Context(), "ocel_app_accounts")

			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
				t.Fatalf("makeRole() = %v, want a %s refusal", err, refusal.CodeNotReady)
			}
			for _, want := range []string{"projects/acme-prod/roles/ocel_app_accounts", "44 days", "OCEL_NAMESPACE"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestTheAppAccountsRoleIDFitsTheLongestNamespaceAnAccountAllows(t *testing.T) {
	t.Parallel()

	width := maxAccountID - 1 - accountHashLen
	namespace := strings.Repeat("a-", width/2) + strings.Repeat("b", width%2)
	if len(namespace) != width || !strings.Contains(namespace, "-") {
		t.Fatalf("the test namespace %q is %d characters, want %d with dashes", namespace, len(namespace), width)
	}
	names := Names{namespace: provider.Namespace(namespace), project: "acme-prod"}

	id := names.AppAccountsRole()

	if !regexp.MustCompile(`^[a-zA-Z0-9_.]{3,64}$`).MatchString(id) {
		t.Errorf("AppAccountsRole() = %q, want 3 to 64 letters, digits, underscores and periods", id)
	}
	if got := names.AppAccountsRolePath(); got != "projects/acme-prod/roles/"+id {
		t.Errorf("AppAccountsRolePath() = %q, want it under the project", got)
	}
}

func TestRemovingABootstrapKeepsTheAppAccountsRole(t *testing.T) {
	t.Parallel()

	names := Names{namespace: "ocel", project: "acme-prod"}
	target := item{Kind: KindRole, Name: names.AppAccountsRole(), Shared: true}
	for _, sibling := range []bool{false, true} {
		read := survey{Tier: environment.TierProduction, Names: names, sibling: sibling, present: map[string]bool{target.ID(): true}}

		taking := removing(read, target)

		if taking.action != provider.ActionKeep || taking.reason != reasonRoleKept {
			t.Errorf("removing the role with sibling=%t = %s %q, want keep %q", sibling, taking.action, taking.reason, reasonRoleKept)
		}
	}
}

func TestAnEmulatedBootstrapMakesNoCustomRole(t *testing.T) {
	t.Parallel()

	names := Names{namespace: "ocel", project: "acme-prod"}
	count := func(emulated bool) int {
		return len(slices.DeleteFunc(bootstrapItems(names, environment.TierProduction, emulated), func(each item) bool { return each.Kind != KindRole }))
	}
	if got := count(true); got != 0 {
		t.Errorf("an emulated bootstrap names %d custom roles, want none: floci answers 404 to every roles call", got)
	}
	if got := count(false); got != 1 {
		t.Errorf("a bootstrap against Google names %d custom roles, want one", got)
	}
}

func TestABootstrapCredentialMayKeepTheCustomRoleItMakes(t *testing.T) {
	t.Parallel()

	document, err := Credentials{Project: Named("acme-prod"), Namespace: "ocel"}.Permissions(edge.PurposeBootstrap)
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
		if !slices.Contains(permissionsFor(nil), want) {
			t.Errorf("a bootstrap does not check %s, and the apply would fail at the role", want)
		}
	}
}
