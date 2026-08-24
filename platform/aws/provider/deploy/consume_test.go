package deploy

import (
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestAnUnscopedPublishedGrantIsRefusedBeforeAnyCloudCall(t *testing.T) {
	t.Parallel()

	var unscoped *UnscopedGrantError
	err := VerifyGrants(providerkit.Link{
		Name:   "db--main",
		Grants: []providerkit.Grant{{Label: "everything", Actions: []string{"s3:*"}, Resources: []string{"*"}}},
	})
	if !errors.As(err, &unscoped) {
		t.Fatalf("VerifyGrants = %v, want an *UnscopedGrantError: a publisher may not hand an app blanket access", err)
	}
}
