package bastion_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
)

func releaseNothing(context.Context, environment.Tier, progress.Log) error { return nil }

func TestTheBastionPartIsReportedOnlyOnceAPortForwardHasMadeIt(t *testing.T) {
	t.Parallel()

	account := newAccount()
	part := bastion.NewPart(account.clients(), testNamespace, "eu-west-2", releaseNothing)
	if _, found, err := part.Describe(context.Background(), environment.TierProduction); err != nil || found {
		t.Fatalf("Describe() = %v, %v, want no bastion reported before a port forward made one", found, err)
	}

	clients, reconciled := readyBastion(t, account)
	if _, err := reconciled.Run(context.Background(), clients); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	got, found, err := part.Describe(context.Background(), environment.TierProduction)
	if err != nil || !found || len(got.Resources) == 0 || got.Resources[0].ID != bastion.NameFor("ocel", environment.TierProduction) {
		t.Errorf("bastion = %+v, %v, %v, want it reported with its cluster", got, found, err)
	}
}

func TestTearingDownTheTierRemovesItsBastion(t *testing.T) {
	t.Parallel()

	account := newAccount()
	clients, reconciled := readyBastion(t, account)
	if _, err := reconciled.Run(context.Background(), clients); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if err := bastion.NewPart(clients, testNamespace, "eu-west-2", releaseNothing).Teardown.Remove(context.Background(), environment.TierProduction, nil); err != nil {
		t.Fatalf("Teardown.Remove() = %v", err)
	}
	if len(account.clusters) != 0 {
		t.Errorf("tearing the tier down left clusters %v", account.clusters)
	}
}

func TestRemovingTheBastionLetsGoOfTheTiersNetworkOnlyOnceItIsGone(t *testing.T) {
	t.Parallel()

	account := newAccount()
	clients, reconciled := readyBastion(t, account)
	if _, err := reconciled.Run(context.Background(), clients); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	var released []environment.Tier
	release := func(_ context.Context, tier environment.Tier, _ progress.Log) error {
		if len(account.clusters) != 0 {
			t.Errorf("the network was let go of while the bastion still had clusters %v in it", account.clusters)
		}
		released = append(released, tier)
		return nil
	}
	if err := bastion.NewPart(clients, testNamespace, "eu-west-2", release).Removal.Remove(context.Background(), environment.TierProduction, nil); err != nil {
		t.Fatalf("Removal.Remove() = %v", err)
	}
	if len(released) != 1 || released[0] != environment.TierProduction {
		t.Errorf("released %v, want the production tier's network let go of once", released)
	}
}
