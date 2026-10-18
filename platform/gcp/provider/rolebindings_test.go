package gcp

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
)

func TestBindingKeyRolesReadsThePolicyAgainWhenItChangedUnderTheWrite(t *testing.T) {
	t.Parallel()
	server := grantedIAM()
	server.keyRaces = 1
	c := server.open(t)
	member := "serviceAccount:app@acme-prod.iam.gserviceaccount.com"
	key := gcloud.KeyPath(c, string(environment.TierProduction))

	changed, err := gcloud.BindKeyRoles(context.Background(), c, environment.TierProduction, member, syncKeyRoles, syncKeyRoles)

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
	if err := gcloud.BindProjectRole(context.Background(), c, member, grants.RecordsRole, nil, true); err != nil {
		t.Fatalf("bindProjectRole() = %v, want the grant to land after the 412", err)
	}
	if got, _ := granting.projectMembers(grants.RecordsRole); !slices.Contains(got, member) {
		t.Errorf("%s has members %q, want %s", grants.RecordsRole, got, member)
	}

}
