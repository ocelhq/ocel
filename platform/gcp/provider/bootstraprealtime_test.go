package gcp

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

func TestATiersRealtimeAccountMayReadThatTiersRealtimeKeysAndNoOtherSecret(t *testing.T) {
	t.Parallel()
	server := grantedIAM()
	b := bootstrap{clients: server.open(t)}
	tier := environment.TierProduction
	account := b.clients.RealtimeAccount(tier)
	ctx := context.Background()

	if err := b.makeAccount(ctx, survey{Tier: tier, Names: b.clients.Names}, account); err != nil {
		t.Fatalf("makeAccount() = %v", err)
	}
	members, condition := server.projectMembers(realtimeKeysReaderRole)
	if member := "serviceAccount:" + b.clients.RealtimeAccountEmail(tier); !slices.Contains(members, member) {
		t.Errorf("the project binds %v to %s, want %s: the gateway mounts its environment's keys", members, realtimeKeysReaderRole, member)
	}
	const want = `resource.type == "secretmanager.googleapis.com/SecretVersion" && ` +
		`resource.name.startsWith("projects/123456789/secrets/ocel_realtime-keys-production-")`
	if condition == nil || condition.Expression != want {
		t.Errorf("the read is conditioned on %+v, want %s: unconditioned, the gateway reads every secret in the project", condition, want)
	}

	found, err := b.accountPresence(ctx, tier, account)
	if err != nil {
		t.Fatalf("accountPresence() = %v", err)
	}
	if !found.present || found.mends != "" {
		t.Errorf("accountPresence() = %+v once the read is granted, want it present with nothing to mend", found)
	}
}

func TestARealtimeAccountThatMayNotReadItsKeysIsSurveyedAsMendable(t *testing.T) {
	t.Parallel()
	server := grantedIAM()
	b := bootstrap{clients: server.open(t)}
	tier := environment.TierProduction

	found, err := b.accountPresence(context.Background(), tier, b.clients.RealtimeAccount(tier))
	if err != nil {
		t.Fatalf("accountPresence() = %v", err)
	}
	purpose, err := b.purposeOf(tier, b.clients.RealtimeAccount(tier))
	if err != nil {
		t.Fatalf("purposeOf() = %v", err)
	}
	if !found.present || found.mends != purpose.ungranted {
		t.Errorf("accountPresence() = %+v, want it present and mended for the read it lacks: a gateway mounting keys it may not read never starts", found)
	}
}

func TestRemovingTheRealtimeAccountTakesItsReadOffTheProject(t *testing.T) {
	t.Parallel()
	server := grantedIAM()
	b := bootstrap{clients: server.open(t)}
	tier := environment.TierProduction
	account := b.clients.RealtimeAccount(tier)
	ctx := context.Background()

	if err := b.makeAccount(ctx, survey{Tier: tier, Names: b.clients.Names}, account); err != nil {
		t.Fatalf("makeAccount() = %v", err)
	}
	if err := b.takeAccount(ctx, tier, account); err != nil {
		t.Fatalf("takeAccount() = %v", err)
	}
	if members, _ := server.projectMembers(realtimeKeysReaderRole); len(members) > 0 {
		t.Errorf("the project still binds %v to %s once the account is gone", members, realtimeKeysReaderRole)
	}
}

func TestAnAccountNoPurposeNamesIsRefusedRatherThanTreatedAsTheRealtimeAccount(t *testing.T) {
	t.Parallel()
	server := grantedIAM()
	b := bootstrap{clients: server.open(t)}
	tier := environment.TierProduction
	ctx := context.Background()

	if err := b.makeAccount(ctx, survey{Tier: tier, Names: b.clients.Names}, "ocel-unheard-of"); err == nil {
		t.Error("makeAccount() = nil, want a refusal: no purpose names that account")
	}
	if err := b.takeAccount(ctx, tier, "ocel-unheard-of"); err == nil {
		t.Error("takeAccount() = nil, want a refusal: realtime's forget must not run for it")
	}
	if len(server.created) != 0 {
		t.Errorf("created %d accounts, want none", len(server.created))
	}
}
