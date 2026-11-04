package endpoint_test

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/endpoint"
	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
)

func TestACallerHoldsTheScopesItsKindGrantsAndNoOthers(t *testing.T) {
	t.Parallel()

	person, err := endpoint.NewUserCaller("https://console.example.com", "org-1", "user-1", scopev1.Role_ROLE_MEMBER, []scopev1.Scope{scopev1.Scope_SCOPE_APPS_READ})
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		caller endpoint.Caller
		kind   endpoint.CallerKind
		holds  []scopev1.Scope
	}{
		"a person":   {person, endpoint.KindUser, []scopev1.Scope{scopev1.Scope_SCOPE_APPS_READ}},
		"a job":      {endpoint.NewJobCaller("https://console.example.com", "org-1", endpoint.Job{ID: "job-1"}), endpoint.KindJob, []scopev1.Scope{scopev1.Scope_SCOPE_JOB}},
		"credential": {endpoint.NewCredentialCaller("https://console.example.com", "org-1", endpoint.TargetCredential{TokenID: "jti"}), endpoint.KindCredential, []scopev1.Scope{scopev1.Scope_SCOPE_TARGET_CREDENTIAL}},
		"nobody":     {endpoint.Caller{OrganizationID: "org-1"}, 0, nil},
	} {
		if tc.caller.Kind() != tc.kind || !slices.Equal(tc.caller.Scopes(), tc.holds) {
			t.Errorf("%s is a %v holding %v, want a %v holding %v", name, tc.caller.Kind(), tc.caller.Scopes(), tc.kind, tc.holds)
		}
		_, isJob := tc.caller.Job()
		_, isCredential := tc.caller.Credential()
		if isJob != (tc.kind == endpoint.KindJob) || isCredential != (tc.kind == endpoint.KindCredential) {
			t.Errorf("%s answers job %v and credential %v, want only what its kind is", name, isJob, isCredential)
		}
	}
}

func TestNoPersonCanBeBuiltHoldingAScopeOnlyATokensKindGrants(t *testing.T) {
	t.Parallel()

	for _, held := range []scopev1.Scope{scopev1.Scope_SCOPE_JOB, scopev1.Scope_SCOPE_TARGET_CREDENTIAL, scopev1.Scope_SCOPE_UNSPECIFIED} {
		if _, err := endpoint.NewUserCaller("https://console.example.com", "org-1", "user-1", scopev1.Role_ROLE_MEMBER, []scopev1.Scope{held}); err == nil {
			t.Errorf("NewUserCaller holding %v = nil, want it refused", held)
		}
	}
}

func TestAPersonHoldsTheRoleTheirTokenNamesAndNoHigher(t *testing.T) {
	t.Parallel()

	at := consoleServing(t)
	tokens, err := endpoint.NewConsoleTokens(at.origin, endpointID, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		role  any
		holds []scopev1.Role
	}{
		"a member":       {"member", []scopev1.Role{scopev1.Role_ROLE_MEMBER}},
		"an admin":       {"admin", []scopev1.Role{scopev1.Role_ROLE_MEMBER, scopev1.Role_ROLE_ADMIN}},
		"an owner":       {"owner", []scopev1.Role{scopev1.Role_ROLE_MEMBER, scopev1.Role_ROLE_ADMIN, scopev1.Role_ROLE_OWNER}},
		"no role":        {nil, nil},
		"an unknown one": {"superuser", nil},
	} {
		claims := map[string]any{"act": "user-1", "scope": []string{"apps.write"}}
		if tc.role != nil {
			claims["role"] = tc.role
		}
		caller, err := tokens.Verify(t.Context(), "Bearer "+at.token(t, minted{audience: endpointID, subject: "org:" + organizationID, claims: claims}))
		if err != nil {
			t.Fatalf("Verify() of %s's token error = %v", name, err)
		}
		for _, role := range []scopev1.Role{scopev1.Role_ROLE_UNSPECIFIED, scopev1.Role_ROLE_MEMBER, scopev1.Role_ROLE_ADMIN, scopev1.Role_ROLE_OWNER} {
			if want := slices.Contains(tc.holds, role); caller.HoldsRole(role) != want {
				t.Errorf("%s HoldsRole(%v) = %v, want %v", name, role, caller.HoldsRole(role), want)
			}
		}
	}
	job := endpoint.NewJobCaller("https://console.example.com", "org-1", endpoint.Job{ID: "job-1"})
	if job.HoldsRole(scopev1.Role_ROLE_MEMBER) {
		t.Error("a job holds a person's role")
	}
}
