package main

import (
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

func TestTheConnectorProvesItsIdentityAsItsOwnServiceAccount(t *testing.T) {
	if envVars(&ports.Clients{}).ProveIdentity == nil {
		t.Fatal("the connector's backend has no ProveIdentity, so an Infisical env source with identity auth is refused through the console")
	}
}
