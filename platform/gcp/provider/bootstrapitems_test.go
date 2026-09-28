package gcp

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

func TestTheItemDigestTellsTwoNamespacesApart(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	here := Names{namespace: "ocel", project: "acme-prod"}
	beside := Names{namespace: "beside", project: "acme-prod"}

	if digestOf(here.Namespace(), bootstrapItems(here, tier, false)) == digestOf(beside.Namespace(), bootstrapItems(beside, tier, false)) {
		t.Error("two namespaces digest the same, so a bootstrap under one would read as current under the other")
	}
	if digestOf(here.Namespace(), bootstrapItems(here, tier, false)) == digestOf(beside.Namespace(), bootstrapItems(here, tier, false)) {
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

	tier := environment.TierProduction
	names := Names{namespace: "ocel", project: "acme-prod"}

	onGoogle := kindsOf(bootstrapItems(names, tier, false))
	if onGoogle[KindRepository] != names.Repository(tier) {
		t.Errorf("a bootstrap against Google names %q as its repository, want %q", onGoogle[KindRepository], names.Repository(tier))
	}
	if emulated := kindsOf(bootstrapItems(names, tier, true)); emulated[KindRepository] != "" {
		t.Errorf("a bootstrap against the emulator names the repository %q, and no emulator serves Artifact Registry: the apply would stop on it",
			emulated[KindRepository])
	}
}

func TestTheRuntimeAccountIsProvisionedWhereverTheBootstrapIs(t *testing.T) {
	t.Parallel()

	tier := environment.TierPreview
	names := Names{namespace: "ocel", project: "acme-prod"}

	for _, emulated := range []bool{false, true} {
		account := item{Kind: KindServiceAccount, Name: names.WorkloadAccount(tier)}
		if ids := idsOf(bootstrapItems(names, tier, emulated)); !slices.Contains(ids, account.ID()) {
			t.Errorf("a bootstrap with emulated=%t provisions %v, want %s among them: an app has to run as something wherever it runs",
				emulated, ids, account.ID())
		}
	}
}
