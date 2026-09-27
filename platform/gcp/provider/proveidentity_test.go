package gcp_test

import (
	"testing"

	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

func TestAGCPTargetProvesItsIdentityAsItsOwnServiceAccount(t *testing.T) {
	p := newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"})

	if p.Hooks().ProveIdentity == nil {
		t.Fatal("the gcp provider sets no ProveIdentity hook, so every Infisical env source with identity auth is refused on a gcp target")
	}
}
