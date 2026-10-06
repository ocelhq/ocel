package gcp

import (
	"context"
	"slices"
	"testing"

	"google.golang.org/api/cloudresourcemanager/v1"

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
