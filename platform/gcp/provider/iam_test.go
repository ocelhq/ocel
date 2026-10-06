package gcp

import (
	"context"
	"slices"
	"testing"

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
