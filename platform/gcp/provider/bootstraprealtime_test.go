package gcp

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func TestATiersRealtimeAccountMayReadThatTiersRealtimeKeysAndNoOtherSecret(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	b := composed(bootstrap.Engine{Clients: server.Open(t)})
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

func TestARealtimeAccountThatMayNotReadItsKeysIsSurveyedAsMendable(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	b := composed(bootstrap.Engine{Clients: server.Open(t)})
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
	b := composed(bootstrap.Engine{Clients: server.Open(t)})
	tier := environment.TierProduction
	account := b.Clients.RealtimeAccount(tier)
	ctx := context.Background()

	if err := b.MakeAccount(ctx, bootstrap.Survey{Tier: tier, Names: b.Clients.Names}, account); err != nil {
		t.Fatalf("makeAccount() = %v", err)
	}
	if err := b.TakeAccount(ctx, tier, account); err != nil {
		t.Fatalf("takeAccount() = %v", err)
	}
	if members, _ := server.ProjectMembers(realtimeKeysReaderRole); len(members) > 0 {
		t.Errorf("the project still binds %v to %s once the account is gone", members, realtimeKeysReaderRole)
	}
}

func TestAnAccountNoPurposeNamesIsRefusedRatherThanTreatedAsTheRealtimeAccount(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	b := composed(bootstrap.Engine{Clients: server.Open(t)})
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
