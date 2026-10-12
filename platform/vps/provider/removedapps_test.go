package vps_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func takenDown(machine *box, container string) bool {
	return slices.ContainsFunc(machine.commands(), func(command string) bool {
		return strings.Contains(command, "docker rm --force '"+container+"'")
	})
}

func TestRetiringRemovedAppsTakesDownTheWorkersOfAnAppNoLongerDeployed(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, store := recording(machine)
	if _, err := p.ProvisionContainers(context.Background(), aStack(t, aWorkerApp()), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	worker, _ := recordedWorker(t, store, "worker")
	hooks := p.Hooks().RemovedApps
	if hooks == nil {
		t.Fatal("the box's hooks leave RemovedApps nil, so an app removed from config keeps its workers consuming its topics")
	}
	live := provider.LiveApps{Slug: "shop", Tier: environment.TierProduction, Env: "prod", Apps: []string{"admin"}}

	plan, err := hooks.Plan(context.Background(), live, progress.Discard())
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if err := hooks.Retire(context.Background(), live, progress.Discard()); err != nil {
		t.Fatalf("Retire() = %v", err)
	}

	var planned []string
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			planned = append(planned, change.Name)
		}
	}
	if !slices.Contains(planned, worker.Container) {
		t.Errorf("the plan removes %v, want web's worker container %s among them", planned, worker.Container)
	}
	for _, name := range []string{"worker", "ledger"} {
		if _, found := recordedWorker(t, store, name); found {
			t.Errorf("worker %s is still recorded, so the engine keeps delivering to an app no longer deployed", name)
		}
	}
	if !takenDown(machine, worker.Container) {
		t.Errorf("worker container %s was never taken down", worker.Container)
	}
}

func TestRetiringRemovedAppsKeepsTheWorkersOfALiveApp(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, store := recording(machine)
	if _, err := p.ProvisionContainers(context.Background(), aStack(t, aWorkerApp()), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	live := provider.LiveApps{Slug: "shop", Tier: environment.TierProduction, Env: "prod", Apps: []string{"web"}}

	plan, err := p.Hooks().RemovedApps.Plan(context.Background(), live, progress.Discard())
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if err := p.Hooks().RemovedApps.Retire(context.Background(), live, progress.Discard()); err != nil {
		t.Fatalf("Retire() = %v", err)
	}

	if len(plan.Groups) != 0 {
		t.Errorf("the plan removes %v, want nothing while web is live", plan.Groups)
	}
	if _, found := recordedWorker(t, store, "worker"); !found {
		t.Error("the live app's worker is no longer recorded")
	}
}
