package gcp

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/envsource"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func TestBindingKeyRolesReadsThePolicyAgainWhenItChangedUnderTheWrite(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	server.KeyRaces = 1
	c := server.Open(t)
	member := "serviceAccount:app@acme-prod.iam.gserviceaccount.com"
	key := gcloud.KeyPath(c, string(environment.TierProduction))

	changed, err := gcloud.BindKeyRoles(context.Background(), c, environment.TierProduction, member, envsource.SyncKeyRoles, envsource.SyncKeyRoles)

	if err != nil || !changed {
		t.Fatalf("bindKeyRoles() = %v, %v, want the grant to land after the policy changed under the first write", changed, err)
	}
	for _, role := range envsource.SyncKeyRoles {
		if got := server.KeyMembers(key, role); !slices.Contains(got, member) {
			t.Errorf("%s has members %q, want %s", role, got, member)
		}
	}
	if got := server.KeyMembers(key, "roles/cloudkms.viewer"); !slices.Equal(got, []string{"user:concurrent"}) {
		t.Errorf("the concurrent binding has members %q, want it kept: the retry reads the policy again", got)
	}
}

func TestWritingTheProjectPolicyReadsItAgainWhenAnEtagMismatchRefusesTheWrite(t *testing.T) {
	t.Parallel()
	member := "serviceAccount:app@acme-prod.iam.gserviceaccount.com"

	granting := googletest.GrantedIAM()
	granting.ProjectStale = 1
	c := granting.Open(t)
	if err := gcloud.BindProjectRole(context.Background(), c, member, credential.RecordsRole, nil, true); err != nil {
		t.Fatalf("bindProjectRole() = %v, want the grant to land after the 412", err)
	}
	if got, _ := granting.ProjectMembers(credential.RecordsRole); !slices.Contains(got, member) {
		t.Errorf("%s has members %q, want %s", credential.RecordsRole, got, member)
	}

}
