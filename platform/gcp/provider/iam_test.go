package gcp

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"cloud.google.com/go/iam/apiv1/iampb"
	"google.golang.org/api/cloudresourcemanager/v1"
	iam "google.golang.org/api/iam/v1"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/environment"
)

func TestBindingKeyRolesReadsThePolicyAgainWhenItChangedUnderTheWrite(t *testing.T) {
	t.Parallel()
	server := grantedIAM()
	server.keyRaces = 1
	c := server.open(t)
	member := "serviceAccount:app@acme-prod.iam.gserviceaccount.com"
	key := keyPath(c, string(environment.TierProduction))

	changed, err := c.bindKeyRoles(context.Background(), environment.TierProduction, member, syncKeyRoles, syncKeyRoles)

	if err != nil || !changed {
		t.Fatalf("bindKeyRoles() = %v, %v, want the grant to land after the policy changed under the first write", changed, err)
	}
	for _, role := range syncKeyRoles {
		if got := server.keyMembers(key, role); !slices.Contains(got, member) {
			t.Errorf("%s has members %q, want %s", role, got, member)
		}
	}
	if got := server.keyMembers(key, "roles/cloudkms.viewer"); !slices.Equal(got, []string{"user:concurrent"}) {
		t.Errorf("the concurrent binding has members %q, want it kept: the retry reads the policy again", got)
	}
}

func TestWritingTheProjectPolicyReadsItAgainWhenAnEtagMismatchRefusesTheWrite(t *testing.T) {
	t.Parallel()
	member := "serviceAccount:app@acme-prod.iam.gserviceaccount.com"

	granting := grantedIAM()
	granting.projectStale = 1
	c := granting.open(t)
	if err := c.bindProjectRole(context.Background(), member, appRecordsRole, nil, true); err != nil {
		t.Fatalf("bindProjectRole() = %v, want the grant to land after the 412", err)
	}
	if got, _ := granting.projectMembers(appRecordsRole); !slices.Contains(got, member) {
		t.Errorf("%s has members %q, want %s", appRecordsRole, got, member)
	}

	revoking := grantedIAM()
	revoking.project.Bindings = []*cloudresourcemanager.Binding{{Role: appRecordsRole, Members: []string{member}}}
	revoking.projectStale = 1
	c = revoking.open(t)
	removed, err := c.unbindProjectMember(context.Background(), member)
	if err != nil || len(removed) != 1 {
		t.Fatalf("unbindProjectMember() = %v, %v, want the revoke to land after the 412", removed, err)
	}
	if got, _ := revoking.projectMembers(appRecordsRole); slices.Contains(got, member) {
		t.Errorf("%s still has members %q, want %s revoked", appRecordsRole, got, member)
	}
}

func policyBindings(members ...string) []*cloudresourcemanager.Binding {
	return []*cloudresourcemanager.Binding{
		{Role: "roles/viewer", Members: slices.Clone(members)},
		{Role: "roles/editor", Members: []string{"user:kept@acme.example", "user:other@acme.example"}},
	}
}

func snapshotOf(t *testing.T, bindings any) string {
	t.Helper()
	encoded, err := json.Marshal(bindings)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestGrantingAMemberARoleLeavesTheBindingsItWasGivenUnchanged(t *testing.T) {
	t.Parallel()
	bindings := policyBindings("user:kept@acme.example")
	before := snapshotOf(t, bindings)

	got, changed := boundMember(bindings, "roles/viewer", "user:new@acme.example", nil, true)

	if !changed || !slices.Contains(got[0].Members, "user:new@acme.example") {
		t.Fatalf("boundMember() = %s, %v, want the member granted", snapshotOf(t, got), changed)
	}
	if after := snapshotOf(t, bindings); after != before {
		t.Errorf("the bindings it was given are now %s, want %s", after, before)
	}
}

func TestTakingAMemberOffARoleLeavesTheBindingsItWasGivenUnchanged(t *testing.T) {
	t.Parallel()
	bindings := policyBindings("user:kept@acme.example", "user:gone@acme.example")
	before := snapshotOf(t, bindings)

	got, changed := boundMember(bindings, "roles/viewer", "user:gone@acme.example", nil, false)

	if !changed || slices.Contains(got[0].Members, "user:gone@acme.example") {
		t.Fatalf("boundMember() = %s, %v, want the member taken off", snapshotOf(t, got), changed)
	}
	if after := snapshotOf(t, bindings); after != before {
		t.Errorf("the bindings it was given are now %s, want %s", after, before)
	}
}

func TestBoundMembersLeavesTheMembersItWasGivenUnchanged(t *testing.T) {
	t.Parallel()
	members := make([]string, 0, 8)
	members = append(members, "user:a@acme.example", "user:b@acme.example", "user:c@acme.example")
	before := slices.Clone(members)

	granted, _ := boundMembers(members, "user:d@acme.example", true)
	revoked, _ := boundMembers(members, "user:a@acme.example", false)

	if !slices.Equal(members, before) || !slices.Equal(members[:cap(members)][:3], before) {
		t.Errorf("the members it was given are now %v, want %v", members, before)
	}
	if !slices.Equal(granted, append(slices.Clone(before), "user:d@acme.example")) || !slices.Equal(revoked, before[1:]) {
		t.Errorf("boundMembers() granted %v and revoked %v", granted, revoked)
	}
}

func TestTakingAMemberOffRolesLeavesThePolicyItWasGivenUnchanged(t *testing.T) {
	t.Parallel()
	bindings := policyBindings("user:kept@acme.example", "user:gone@acme.example")
	before := snapshotOf(t, bindings)

	kept, removed := removeMemberFromRoles(bindings, "user:gone@acme.example", []string{"roles/viewer"})

	if len(removed) != 1 || slices.Contains(kept[0].Members, "user:gone@acme.example") {
		t.Fatalf("removeMemberFromRoles() = %s, %s, want the member off roles/viewer", snapshotOf(t, kept), snapshotOf(t, removed))
	}
	if after := snapshotOf(t, bindings); after != before {
		t.Errorf("the policy it was given is now %s, want %s", after, before)
	}
}

func TestGrantingAMemberAKeyRoleLeavesTheBindingsItWasGivenUnchanged(t *testing.T) {
	t.Parallel()
	bindings := []*iampb.Binding{{Role: "roles/cloudkms.viewer", Members: []string{"user:kept@acme.example"}}}
	before := proto.Clone(bindings[0]).(*iampb.Binding)

	got, changed := boundKeyMember(bindings, "roles/cloudkms.viewer", "user:new@acme.example", true)

	if !changed || !slices.Contains(got[0].GetMembers(), "user:new@acme.example") {
		t.Fatalf("boundKeyMember() changed %v, want the member granted", changed)
	}
	if !proto.Equal(bindings[0], before) {
		t.Errorf("the binding it was given is now %s %v, want %s %v", bindings[0].GetRole(), bindings[0].GetMembers(), before.GetRole(), before.GetMembers())
	}
}

func TestGrantingAMemberAnAccountRoleLeavesTheBindingsItWasGivenUnchanged(t *testing.T) {
	t.Parallel()
	bindings := []*iam.Binding{{Role: "roles/iam.serviceAccountUser", Members: []string{"user:kept@acme.example"}}}
	before := snapshotOf(t, bindings)

	got, changed := boundAccountMember(bindings, "roles/iam.serviceAccountUser", "user:new@acme.example", true)

	if !changed || !slices.Contains(got[0].Members, "user:new@acme.example") {
		t.Fatalf("boundAccountMember() = %v, %v, want the member granted", got, changed)
	}
	if after := snapshotOf(t, bindings); after != before {
		t.Errorf("the bindings it was given are now %s, want %s", after, before)
	}
}

func TestTakingAMemberOffTheProjectReportsEveryBindingItHeld(t *testing.T) {
	t.Parallel()
	member, other := "serviceAccount:app@x.iam.gserviceaccount.com", "serviceAccount:other@x.iam.gserviceaccount.com"
	first := &cloudresourcemanager.Expr{Expression: "a"}
	second := &cloudresourcemanager.Expr{Expression: "b"}
	bindings := []*cloudresourcemanager.Binding{
		{Role: "roles/one", Condition: first, Members: []string{member}},
		{Role: "roles/two", Condition: second, Members: []string{member, other}},
		{Role: "roles/three", Members: []string{member}},
		{Role: "roles/four", Members: []string{other}},
		{Role: "roles/five", Members: []string{member}},
	}

	kept, removed := removeMemberFromRoles(bindings, member, []string{"roles/one", "roles/two", "roles/three"})

	if len(removed) != 3 {
		t.Fatalf("removeMemberFromRoles() removed %+v, want three bindings", removed)
	}
	for i, want := range []struct {
		role string
		cond *cloudresourcemanager.Expr
	}{{"roles/one", first}, {"roles/two", second}, {"roles/three", nil}} {
		if removed[i].Role != want.role || removed[i].Condition != want.cond || !slices.Equal(removed[i].Members, []string{member}) {
			t.Errorf("removed[%d] = %+v, want %s with its condition and exactly the member", i, removed[i], want.role)
		}
	}
	if len(kept) != 3 || !slices.Equal(kept[0].Members, []string{other}) || kept[0].Role != "roles/two" || kept[2].Role != "roles/five" || !slices.Equal(kept[2].Members, []string{member}) {
		t.Errorf("removeMemberFromRoles() kept %+v, want roles/two held by the other member, roles/four and roles/five still held by the member", kept)
	}
}
