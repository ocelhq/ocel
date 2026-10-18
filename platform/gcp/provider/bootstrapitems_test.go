package gcp

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func TestTheItemDigestTellsTwoNamespacesApart(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	here := names.New("ocel", "acme-prod")
	beside := names.New("beside", "acme-prod")

	if bootstrap.DigestOf(here.Namespace(), bootstrap.Resources(here, tier, false)) == bootstrap.DigestOf(beside.Namespace(), bootstrap.Resources(beside, tier, false)) {
		t.Error("two namespaces digest the same, so a bootstrap under one would read as current under the other")
	}
	if bootstrap.DigestOf(here.Namespace(), bootstrap.Resources(here, tier, false)) == bootstrap.DigestOf(beside.Namespace(), bootstrap.Resources(here, tier, false)) {
		t.Error("the digest ignores the namespace it was written under, and the item names alone are what it turns on")
	}
}

func kindsOf(items []bootstrap.Resource) map[bootstrap.Kind]string {
	named := map[bootstrap.Kind]string{}
	for _, item := range items {
		named[item.Kind] = item.Name
	}
	return named
}

func TestTheEmulatorLeavesOutTheRepositoryItDoesNotServe(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	names := names.New("ocel", "acme-prod")

	onGoogle := kindsOf(bootstrap.Resources(names, tier, false))
	if onGoogle[bootstrap.KindRepository] != names.Repository(tier) {
		t.Errorf("a bootstrap against Google names %q as its repository, want %q", onGoogle[bootstrap.KindRepository], names.Repository(tier))
	}
	if emulated := kindsOf(bootstrap.Resources(names, tier, true)); emulated[bootstrap.KindRepository] != "" {
		t.Errorf("a bootstrap against the emulator names the repository %q, and no emulator serves Artifact Registry: the apply would stop on it",
			emulated[bootstrap.KindRepository])
	}
}

func TestABootstrapNamesNoAccountEveryAppMayActAs(t *testing.T) {
	t.Parallel()

	names := names.New("ocel", "acme-prod")
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		for _, emulated := range []bool{false, true} {
			for _, each := range bootstrap.Resources(names, tier, emulated) {
				if each.Name == "ocel-"+string(tier) {
					t.Errorf("a bootstrap of tier %s with emulated=%t names %s, and an account of the tier's own is one every app would act as", tier, emulated, each.ID())
				}
			}
		}
	}
}

func TestEachTierHasAnAccountItsRealtimeGatewaysRunAs(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	names := names.New("ocel", "acme-prod")
	account := bootstrap.Resource{Kind: bootstrap.KindServiceAccount, Name: names.RealtimeAccount(tier)}
	for _, emulated := range []bool{false, true} {
		if ids := googletest.IdsOf(bootstrap.Resources(names, tier, emulated)); !slices.Contains(ids, account.ID()) {
			t.Errorf("a bootstrap with emulated=%t provisions %v, want %s among them", emulated, ids, account.ID())
		}
	}
}

func TestTheBaseGrantsCloudRunTheNetworkEveryTierSubnetworkIsUsedThrough(t *testing.T) {
	t.Parallel()

	names := names.New("ocel", "acme-prod")
	grant := bootstrap.Resource{Kind: bootstrap.KindGrant, Name: bootstrap.NetworkUserRole}
	for _, tier := range googletest.BothTiers {
		items := bootstrap.Resources(names, tier, false)
		at := slices.IndexFunc(items, func(each bootstrap.Resource) bool { return each.ID() == grant.ID() })
		if at < 0 || !items[at].Shared {
			t.Errorf("a %s bootstrap provisions %v, want %s shared by both tiers: a deploy adding the network may not grant the Cloud Run agent on it", tier, googletest.IdsOf(items), grant.ID())
		}
		if slices.Contains(googletest.IdsOf(bootstrap.Resources(names, tier, true)), grant.ID()) {
			t.Errorf("an emulated %s bootstrap grants %s, and no emulator serves project IAM", tier, grant.ID())
		}
	}
}
