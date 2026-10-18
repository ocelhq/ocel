package realtime

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func TestATiersRealtimeAccountMayReadThatTiersRealtimeKeysAndNoOtherSecret(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	b := bootstrap.Engine{Clients: server.Open(t), Accounts: []bootstrap.Account{{Name: names.Names.RealtimeAccount, Purpose: Purpose}}}
	tier := environment.TierProduction
	account := b.Clients.RealtimeAccount(tier)
	ctx := context.Background()

	if err := b.MakeAccount(ctx, bootstrap.Survey{Tier: tier, Names: b.Clients.Names}, account); err != nil {
		t.Fatalf("makeAccount() = %v", err)
	}
	members, condition := server.ProjectMembers(realtimeKeysReaderRole)
	if member := "serviceAccount:" + b.Clients.RealtimeAccountEmail(tier); !slices.Contains(members, member) {
		t.Errorf("the project binds %v to %s, want %s: the gateway mounts its environment's keys", members, realtimeKeysReaderRole, member)
	}
	const want = `resource.type == "secretmanager.googleapis.com/SecretVersion" && ` +
		`resource.name.startsWith("projects/123456789/secrets/ocel_realtime-keys-production-")`
	if condition == nil || condition.Expression != want {
		t.Errorf("the read is conditioned on %+v, want %s: unconditioned, the gateway reads every secret in the project", condition, want)
	}

	found, err := b.AccountPresence(ctx, tier, account)
	if err != nil {
		t.Fatalf("accountPresence() = %v", err)
	}
	if !found.Present || found.Mends != "" {
		t.Errorf("accountPresence() = %+v once the read is granted, want it present with nothing to mend", found)
	}
}

func TestATiersRealtimeAccountMayHoldSubscriptionsOfItsOwnThroughTheRealtimeSubscriptionsRole(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	b := bootstrap.Engine{Clients: server.Open(t), Accounts: []bootstrap.Account{{Name: names.Names.RealtimeAccount, Purpose: Purpose}}}
	tier := environment.TierProduction
	ctx := context.Background()

	if err := b.MakeAccount(ctx, bootstrap.Survey{Tier: tier, Names: b.Clients.Names}, b.Clients.RealtimeAccount(tier)); err != nil {
		t.Fatalf("makeAccount() = %v", err)
	}
	members, condition := server.ProjectMembers(b.Clients.RealtimeSubscriptionsRolePath())
	if member := "serviceAccount:" + b.Clients.RealtimeAccountEmail(tier); !slices.Contains(members, member) {
		t.Errorf("the project binds %v to %s, want %s: each gateway instance subscribes to its environment's topic itself", members, b.Clients.RealtimeSubscriptionsRolePath(), member)
	}
	if condition != nil {
		t.Errorf("the role is conditioned on %+v, and Pub/Sub honours no IAM condition on a subscription's name, so a condition grants nothing", condition)
	}
}

func TestTheRealtimeSubscriptionsRoleMayCreatePullAndDeleteSubscriptionsAndNothingElse(t *testing.T) {
	t.Parallel()
	named := names.New("ocel", "acme-prod")

	role, found := bootstrap.FindCustomRole(named, named.RealtimeSubscriptionsRole())
	if !found {
		t.Fatalf("no custom role is named %s", named.RealtimeSubscriptionsRole())
	}
	want := []string{"pubsub.subscriptions.consume", "pubsub.subscriptions.create", "pubsub.subscriptions.delete"}
	if got := slices.Sorted(slices.Values(role.Permissions)); !slices.Equal(got, want) {
		t.Errorf("the role holds %v, want %v: attaching to a topic is granted on the realtime topic alone", got, want)
	}
	if !slices.ContainsFunc(bootstrap.StackResources(named, environment.TierProduction, false), func(r bootstrap.Resource) bool {
		return r.Kind == bootstrap.KindRole && r.Name == named.RealtimeSubscriptionsRole()
	}) {
		t.Error("a bootstrap makes no realtime subscriptions role, and the binding its realtime account is granted grants nothing")
	}
}

func TestARealtimeAccountThatMayNotReadItsKeysIsSurveyedAsMendable(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	b := bootstrap.Engine{Clients: server.Open(t), Accounts: []bootstrap.Account{{Name: names.Names.RealtimeAccount, Purpose: Purpose}}}
	tier := environment.TierProduction

	found, err := b.AccountPresence(context.Background(), tier, b.Clients.RealtimeAccount(tier))
	if err != nil {
		t.Fatalf("accountPresence() = %v", err)
	}
	purpose, err := b.PurposeOf(tier, b.Clients.RealtimeAccount(tier))
	if err != nil {
		t.Fatalf("purposeOf() = %v", err)
	}
	if !found.Present || found.Mends != purpose.Ungranted {
		t.Errorf("accountPresence() = %+v, want it present and mended for the read it lacks: a gateway mounting keys it may not read never starts", found)
	}
}

func TestRemovingTheRealtimeAccountTakesItsReadOffTheProject(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	b := bootstrap.Engine{Clients: server.Open(t), Accounts: []bootstrap.Account{{Name: names.Names.RealtimeAccount, Purpose: Purpose}}}
	tier := environment.TierProduction
	account := b.Clients.RealtimeAccount(tier)
	ctx := context.Background()

	if err := b.MakeAccount(ctx, bootstrap.Survey{Tier: tier, Names: b.Clients.Names}, account); err != nil {
		t.Fatalf("makeAccount() = %v", err)
	}
	if err := b.TakeAccount(ctx, tier, account); err != nil {
		t.Fatalf("takeAccount() = %v", err)
	}
	for _, role := range []string{realtimeKeysReaderRole, b.Clients.RealtimeSubscriptionsRolePath()} {
		if members, _ := server.ProjectMembers(role); len(members) > 0 {
			t.Errorf("the project still binds %v to %s once the account is gone", members, role)
		}
	}
}

func TestAnAccountNoPurposeNamesIsRefusedRatherThanTreatedAsTheRealtimeAccount(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	b := bootstrap.Engine{Clients: server.Open(t), Accounts: []bootstrap.Account{{Name: names.Names.RealtimeAccount, Purpose: Purpose}}}
	tier := environment.TierProduction
	ctx := context.Background()

	if err := b.MakeAccount(ctx, bootstrap.Survey{Tier: tier, Names: b.Clients.Names}, "ocel-unheard-of"); err == nil {
		t.Error("makeAccount() = nil, want a refusal: no purpose names that account")
	}
	if err := b.TakeAccount(ctx, tier, "ocel-unheard-of"); err == nil {
		t.Error("takeAccount() = nil, want a refusal: realtime's forget must not run for it")
	}
	if len(server.Created) != 0 {
		t.Errorf("created %d accounts, want none", len(server.Created))
	}
}
