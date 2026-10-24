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

func TestRetiringTakesDownTheWorkersOfAnAppNoLongerDeployed(t *testing.T) {
	t.Parallel()

	machine := &box{remembers: true}
	p, store := recording(machine)
	promotedWorkers(t, p, provisionedRelease(t, p, aStack(t, aWorkerApp())))
	worker, _ := recordedWorker(t, store, "worker")
	hooks := p.Hooks().Retirement
	if hooks == nil {
		t.Fatal("the box's hooks leave Retirement nil, so an app removed from config keeps its workers consuming its topics")
	}
	live := provider.LiveApps{Slug: "shop", Tier: environment.TierProduction, Env: "prod", Apps: []provider.LiveApp{{App: "admin"}}}

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

func TestRetiringKeepsTheWorkersOfALiveApp(t *testing.T) {
	t.Parallel()

	machine := &box{remembers: true}
	p, store := recording(machine)
	promotedWorkers(t, p, provisionedRelease(t, p, aStack(t, aWorkerApp())))
	live := provider.LiveApps{Slug: "shop", Tier: environment.TierProduction, Env: "prod", Apps: []provider.LiveApp{{App: "web", Workers: []string{"worker", "ledger"}}}}

	plan, err := p.Hooks().Retirement.Plan(context.Background(), live, progress.Discard())
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if err := p.Hooks().Retirement.Retire(context.Background(), live, progress.Discard()); err != nil {
		t.Fatalf("Retire() = %v", err)
	}

	if len(plan.Groups) != 0 {
		t.Errorf("the plan removes %v, want nothing while web is live", plan.Groups)
	}
	if _, found := recordedWorker(t, store, "worker"); !found {
		t.Error("the live app's worker is no longer recorded")
	}
}

func TestRetiringTakesDownTheWorkersALiveAppNoLongerHosts(t *testing.T) {
	t.Parallel()

	machine := &box{remembers: true}
	p, store := recording(machine)
	promotedWorkers(t, p, provisionedRelease(t, p, aStack(t, aWorkerApp())))
	dropped, _ := recordedWorker(t, store, "ledger")
	live := provider.LiveApps{Slug: "shop", Tier: environment.TierProduction, Env: "prod", Apps: []provider.LiveApp{{App: "web", Workers: []string{"worker"}}}}

	plan, err := p.Hooks().Retirement.Plan(context.Background(), live, progress.Discard())
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	before := len(machine.commands())
	if err := p.Hooks().Retirement.Retire(context.Background(), live, progress.Discard()); err != nil {
		t.Fatalf("Retire() = %v", err)
	}

	var planned []string
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			planned = append(planned, change.Name)
		}
	}
	if !slices.Equal(planned, []string{dropped.Container}) {
		t.Errorf("the plan removes %v, want ledger's container %s alone", planned, dropped.Container)
	}
	if !slices.ContainsFunc(machine.commands()[before:], func(command string) bool {
		return strings.Contains(command, "docker rm --force '"+dropped.Container+"'")
	}) {
		t.Errorf("ledger's container %s was never taken down", dropped.Container)
	}
	if _, found := recordedWorker(t, store, "ledger"); found {
		t.Error("ledger is still recorded after the promotion that dropped it, so the engine keeps delivering to it")
	}
	if _, found := recordedWorker(t, store, "worker"); !found {
		t.Error("the worker web still hosts is no longer recorded")
	}
}
