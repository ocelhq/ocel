package gcp

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestTheItemDigestTellsTwoNamespacesApart(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	here := Names{namespace: "ocel", project: "acme-prod"}
	beside := Names{namespace: "beside", project: "acme-prod"}

	if digestOf(here.Namespace(), bootstrapItems(here, class)) == digestOf(beside.Namespace(), bootstrapItems(beside, class)) {
		t.Error("two namespaces digest the same, so a bootstrap under one would read as current under the other")
	}
	if digestOf(here.Namespace(), bootstrapItems(here, class)) == digestOf(beside.Namespace(), bootstrapItems(here, class)) {
		t.Error("the digest ignores the namespace it was written under, and the item names alone are what it turns on")
	}
}
