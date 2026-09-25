package gcp_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

func TestAGCPTargetSignsInToInfisicalAsItsOwnServiceAccount(t *testing.T) {
	var held providerkit.Provider = newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"})

	identity, holds := held.(providerkit.EnvSourceIdentity)
	if !holds {
		t.Fatal("the gcp provider offers no env source identity, so every Infisical source with gcp auth is refused on a gcp target")
	}
	if identity.EnvSourceTarget().Issuer == nil {
		t.Error("EnvSourceTarget() carries no identity token issuer, so gcp auth has nothing to sign in with")
	}
}
