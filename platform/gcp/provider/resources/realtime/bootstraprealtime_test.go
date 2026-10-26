package realtime

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func TestATiersRealtimeAccountMayHoldSubscriptionsOnlyWhereItsTiersDeployCredentialReaches(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	b := bootstrap.Engine{ServiceStates: &bootstrap.ServiceStates{}, Clients: server.Open(t), Accounts: []bootstrap.Account{{Name: names.Names.RealtimeAccount, Purpose: Purpose}}}
	tier := environment.TierProduction
	account := b.Clients.RealtimeAccount(tier)
	ctx := context.Background()

	if err := b.MakeAccount(ctx, bootstrap.Survey{Tier: tier, Names: b.Clients.Names}, account); err != nil {
		t.Fatalf("makeAccount() = %v", err)
	}
	members, condition := server.ProjectMembers(credential.RealtimeSubscriptionsRole)
	if member := "serviceAccount:" + b.Clients.RealtimeAccountEmail(tier); !slices.Contains(members, member) {
		t.Errorf("the project binds %v to %s, want %s: each gateway instance subscribes to its environment's topic itself", members, credential.RealtimeSubscriptionsRole, member)
	}
	if want := b.Clients.DeployCredential(tier).TierCondition().Expression; condition == nil || condition.Expression != want {
		t.Errorf("the role is conditioned on %+v, want %s: unconditioned, the gateway reaches the other tier's topics", condition, want)
	}
	if members, _ := server.ProjectMembers(realtimeKeysReaderRole); len(members) > 0 {
		t.Errorf("the project binds %v to %s, want the gateway's read granted on its keys secret alone", members, realtimeKeysReaderRole)
	}

	found, err := b.AccountPresence(ctx, tier, account)
	if err != nil {
		t.Fatalf("accountPresence() = %v", err)
	}
	if !found.Present || found.Mends != "" {
		t.Errorf("accountPresence() = %+v once the role is granted, want it present with nothing to mend", found)
	}
}

func TestTheRealtimeAccountIsGrantedAPredefinedRoleTheDeployCredentialMayGrantAndNoCustomRole(t *testing.T) {
	t.Parallel()
	named := names.New("ocel", "acme-prod")

	if !strings.HasPrefix(credential.RealtimeSubscriptionsRole, "roles/pubsub.") {
		t.Errorf("the realtime account is granted %s, want a predefined Pub/Sub role: a deploy makes the account, and no deploy may make a custom role", credential.RealtimeSubscriptionsRole)
	}
	if !strings.Contains(credential.New(named, "europe-west1", environment.TierProduction).AppGrants().Condition.Expression, "'"+credential.RealtimeSubscriptionsRole+"'") {
		t.Errorf("the deploy credential may not grant %s, and the deploy that adds the realtime part grants it", credential.RealtimeSubscriptionsRole)
	}
	for _, each := range bootstrap.Resources(named, environment.TierProduction, false) {
		if each.Kind == bootstrap.KindRole && strings.Contains(each.Name, "realtime") {
			t.Errorf("a bootstrap makes %s, and the realtime account is granted a predefined role", each.ID())
		}
	}
}

func TestARealtimeAccountThatMayNotHoldItsSubscriptionsIsSurveyedAsMendable(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	b := bootstrap.Engine{ServiceStates: &bootstrap.ServiceStates{}, Clients: server.Open(t), Accounts: []bootstrap.Account{{Name: names.Names.RealtimeAccount, Purpose: Purpose}}}
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
		t.Errorf("accountPresence() = %+v, want it present and mended for the role it lacks: a gateway that may not subscribe never hears its topic", found)
	}
}

func TestRemovingTheRealtimeAccountTakesItsRoleOffTheProject(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	b := bootstrap.Engine{ServiceStates: &bootstrap.ServiceStates{}, Clients: server.Open(t), Accounts: []bootstrap.Account{{Name: names.Names.RealtimeAccount, Purpose: Purpose}}}
	tier := environment.TierProduction
	account := b.Clients.RealtimeAccount(tier)
	ctx := context.Background()

	if err := b.MakeAccount(ctx, bootstrap.Survey{Tier: tier, Names: b.Clients.Names}, account); err != nil {
		t.Fatalf("makeAccount() = %v", err)
	}
	if err := b.TakeAccount(ctx, tier, account); err != nil {
		t.Fatalf("takeAccount() = %v", err)
	}
	if members, _ := server.ProjectMembers(credential.RealtimeSubscriptionsRole); len(members) > 0 {
		t.Errorf("the project still binds %v to %s once the account is gone", members, credential.RealtimeSubscriptionsRole)
	}
}

func TestAnAccountNoPurposeNamesIsRefusedRatherThanTreatedAsTheRealtimeAccount(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	b := bootstrap.Engine{ServiceStates: &bootstrap.ServiceStates{}, Clients: server.Open(t), Accounts: []bootstrap.Account{{Name: names.Names.RealtimeAccount, Purpose: Purpose}}}
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
