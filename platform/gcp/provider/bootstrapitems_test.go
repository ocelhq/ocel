package gcp

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
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

func TestTheDelayAccountIsProvisionedWhereverTheBootstrapIs(t *testing.T) {
	t.Parallel()

	tier := environment.TierPreview
	names := Names{namespace: "ocel", project: "acme-prod"}

	for _, emulated := range []bool{false, true} {
		account := item{Kind: KindServiceAccount, Name: names.DelayAccount(tier)}
		if ids := idsOf(bootstrapItems(names, tier, emulated)); !slices.Contains(ids, account.ID()) {
			t.Errorf("a bootstrap with emulated=%t provisions %v, want %s among them: a delayed message has to be published as something wherever it runs",
				emulated, ids, account.ID())
		}
	}
}

func TestEachTierHasAnAccountItsRealtimeGatewaysRunAs(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	names := Names{namespace: "ocel", project: "acme-prod"}
	account := item{Kind: KindServiceAccount, Name: names.RealtimeAccount(tier)}
	for _, emulated := range []bool{false, true} {
		if ids := idsOf(bootstrapItems(names, tier, emulated)); !slices.Contains(ids, account.ID()) {
			t.Errorf("a bootstrap with emulated=%t provisions %v, want %s among them", emulated, ids, account.ID())
		}
	}
	opened := bootstrap{clients: &clients{Names: names}}
	purpose := opened.purposeOf(tier, account.Name)
	if purpose.description == opened.purposeOf(tier, names.DelayAccount(tier)).description {
		t.Error("the realtime account is described as the account a delayed message is published as")
	}
}

func TestTheTiersDelayAccountIsGrantedNothingAnAppReads(t *testing.T) {
	t.Parallel()
	server := grantedIAM()
	b := bootstrap{clients: server.open(t)}
	read := survey{Tier: environment.TierProduction, Names: b.clients.Names}
	ctx := context.Background()

	if err := b.makeAccount(ctx, read, "ocel-production"); err != nil {
		t.Fatalf("makeAccount() = %v", err)
	}

	const member = "serviceAccount:ocel-production@acme-prod.iam.gserviceaccount.com"
	for _, role := range appGrantedRoles {
		if bound, _ := server.projectMembers(role); slices.Contains(bound, member) {
			t.Errorf("the project binds the delay account to %s, and a delayed message reads nothing", role)
		}
	}
	key := "projects/acme-prod/locations/europe-west1/keyRings/ocel/cryptoKeys/production"
	if server.writes != 0 || len(server.keyMembers(key, appOpeningRole)) != 0 {
		t.Errorf("the bootstrap wrote %d policies and the key binds %v to %s, want no key write", server.writes, server.keyMembers(key, appOpeningRole), appOpeningRole)
	}
	found, err := b.accountPresence(ctx, environment.TierProduction, "ocel-production")
	if err != nil {
		t.Fatalf("accountPresence() = %v", err)
	}
	if !found.present || found.mends != "" {
		t.Errorf("accountPresence() = %+v, want the delay account present with nothing to mend", found)
	}
}

func tagsDatabaseOf(names Names, tier environment.Tier, emulated bool) (item, bool) {
	for _, each := range stackItems(names, tier, emulated) {
		if each.Kind == KindDatabase && each.Name == names.TagDatabase(tier) {
			return each, true
		}
	}
	return item{}, false
}

func TestEveryTierHasATagsDatabaseOfItsOwn(t *testing.T) {
	t.Parallel()

	names := Names{namespace: "ocel", project: "acme-prod"}
	var named []string
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		database, ok := tagsDatabaseOf(names, tier, false)
		if !ok {
			t.Fatalf("the %s tier's stack items = %v, want its tags database among them", tier, idsOf(stackItems(names, tier, false)))
		}
		if database.Shared || !database.Slow {
			t.Errorf("the %s tier's tags database = %+v, want it slow to make and not shared with the sibling tier", tier, database)
		}
		named = append(named, database.Name)
	}
	if named[0] == named[1] {
		t.Errorf("both tiers name the tags database %q, want one each", named[0])
	}
}

func TestTheEmulatorKeepsTheTagsDatabaseItServesImplicitly(t *testing.T) {
	t.Parallel()

	names := Names{namespace: "ocel", project: "acme-prod"}
	tier := environment.TierProduction
	database, ok := tagsDatabaseOf(names, tier, true)
	if !ok {
		t.Fatalf("the emulated stack items = %v, want the tags database among them", idsOf(stackItems(names, tier, true)))
	}
	read := survey{Tier: tier, Names: names, Emulated: true, present: map[string]bool{database.ID(): true}}
	for _, change := range planned(read, []item{database}) {
		if change.Action != provider.ActionKeep || change.Reason != reasonEmulated {
			t.Errorf("planned() = %s %q, want keep %q", change.Action, change.Reason, reasonEmulated)
		}
	}
}
