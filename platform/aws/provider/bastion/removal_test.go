package bastion_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
)

func TestRemoveLeavesNothingOfATiersBastionBehindNotEvenARunningTask(t *testing.T) {
	t.Parallel()

	account := newAccount()
	clients, ensured := readyBastion(t, account)
	if _, err := ensured.Run(context.Background(), clients); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if _, err := ensured.Run(context.Background(), clients); err != nil {
		t.Fatalf("second Run() = %v", err)
	}

	if err := bastion.Remove(context.Background(), clients, environment.TierProduction); err != nil {
		t.Fatalf("Remove() = %v", err)
	}

	if running := account.runningTasks(); len(running) != 0 {
		t.Errorf("tasks %v still run", running)
	}
	if len(account.clusters) != 0 || len(account.roles) != 0 || len(account.groups) != 0 {
		t.Errorf("Remove() left clusters %v, roles %v and %d security groups", account.clusters, account.roles, len(account.groups))
	}
	for family, revisions := range account.definition {
		if len(revisions) != 0 {
			t.Errorf("Remove() left %d revisions of task definition %s: an inactive revision stays in the account until it is deleted", len(revisions), family)
		}
	}
}

func TestRemoveOfATierThatNeverForwardedAPortChangesNothing(t *testing.T) {
	t.Parallel()

	account := newAccount()
	clients := account.clients()

	if err := bastion.Remove(context.Background(), clients, environment.TierPreview); err != nil {
		t.Fatalf("Remove() = %v", err)
	}

	for _, call := range account.calls {
		if strings.HasPrefix(call, "Delete") || strings.HasPrefix(call, "Stop") || strings.HasPrefix(call, "Deregister") {
			t.Errorf("Remove() of nothing called %s", call)
		}
	}
}

func TestRemoveWaitsOutTheNetworkInterfacesAStoppedTaskStillHoldsOnItsSecurityGroup(t *testing.T) {
	t.Parallel()

	account := newAccount()
	clients, ensured := readyBastion(t, account)
	if _, err := ensured.Run(context.Background(), clients); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	account.groupBusy = 2

	if err := bastion.Remove(context.Background(), clients, environment.TierProduction); err != nil {
		t.Fatalf("Remove() = %v", err)
	}

	if len(account.groups) != 0 {
		t.Errorf("%d security groups remain after Remove()", len(account.groups))
	}
}

func TestRemoveLeavesAClusterOcelDidNotTagAlone(t *testing.T) {
	t.Parallel()

	account := newAccount()
	account.clusters["ocel-bastion-production"] = map[string]string{}

	err := bastion.Remove(context.Background(), account.clients(), environment.TierProduction)

	if err == nil {
		t.Fatal("Remove() of a cluster Ocel did not tag = nil error, want it refused")
	}
	if _, kept := account.clusters["ocel-bastion-production"]; !kept {
		t.Error("Remove() deleted a cluster Ocel did not tag")
	}
}
