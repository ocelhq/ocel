package gcp

import (
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestTheItemDigestTellsTwoNamespacesApart(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	here := Names{namespace: "ocel", project: "acme-prod"}
	beside := Names{namespace: "beside", project: "acme-prod"}

	if digestOf(here.Namespace(), bootstrapItems(here, class, false)) == digestOf(beside.Namespace(), bootstrapItems(beside, class, false)) {
		t.Error("two namespaces digest the same, so a bootstrap under one would read as current under the other")
	}
	if digestOf(here.Namespace(), bootstrapItems(here, class, false)) == digestOf(beside.Namespace(), bootstrapItems(here, class, false)) {
		t.Error("the digest ignores the namespace it was written under, and the item names alone are what it turns on")
	}
}

func kindsOf(items []item) map[Kind]string {
	named := map[Kind]string{}
	for _, item := range items {
		named[item.Kind] = item.Name
	}
	return named
}

func TestTheEmulatorLeavesOutTheRepositoryItDoesNotServe(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	names := Names{namespace: "ocel", project: "acme-prod"}

	onGoogle := kindsOf(bootstrapItems(names, class, false))
	if onGoogle[KindRepository] != names.Repository(class) {
		t.Errorf("a bootstrap against Google names %q as its repository, want %q", onGoogle[KindRepository], names.Repository(class))
	}
	if emulated := kindsOf(bootstrapItems(names, class, true)); emulated[KindRepository] != "" {
		t.Errorf("a bootstrap against the emulator names the repository %q, and no emulator serves Artifact Registry: the apply would stop on it",
			emulated[KindRepository])
	}
}

func TestTheRuntimeAccountIsProvisionedWhereverTheBootstrapIs(t *testing.T) {
	t.Parallel()

	class := edge.ClassPreview
	names := Names{namespace: "ocel", project: "acme-prod"}

	for _, emulated := range []bool{false, true} {
		if got := kindsOf(bootstrapItems(names, class, emulated))[KindServiceAccount]; got != names.WorkloadAccount(class) {
			t.Errorf("a bootstrap with emulated=%t names %q as its runtime account, want %q: an app has to run as something wherever it runs",
				emulated, got, names.WorkloadAccount(class))
		}
	}
}
