package providerserver_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestADeployRetiresWhatTheAppsItNoLongerDeploysLeftBehindOnceItsPromotionServes(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	if result, _ := deploy(t, client, twoAppRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() of web and admin = %q", result.GetError())
	}

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() of web alone = %q", result.GetError())
	}

	retirements := vendor.Retirements()
	if len(retirements) != 2 {
		t.Fatalf("the deploys retired %v, want one retirement per promotion", retirements)
	}
	last := retirements[1]
	if last.Slug != "shop" || last.Tier != environment.TierProduction || !reflect.DeepEqual(last.Apps, []provider.LiveApp{{App: "web", Compute: provider.ComputeServerless}}) {
		t.Errorf("the deploy of web alone retired %+v, want shop's production with web the one live app", last)
	}
}

func TestADeployTellsRetirementTheComputeAndWorkersOfEachAppItMakesLive(t *testing.T) {
	builtProject(t)
	vendor := fake.NewProvider(fake.Options{}).WithFacts(runningWorkers)
	client := servedBy(t, vendor)
	bootstrappedOverRPC(t, client)
	req := deployRequest()
	req.Manifest.Workers = []*contractv1.ManifestWorker{{Name: "media", App: "web", Compute: string(provider.ComputeServerless)}}

	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	retirements := vendor.Retirements()
	if len(retirements) != 1 {
		t.Fatalf("the deploy retired %v, want one retirement", retirements)
	}
	if want := []provider.LiveApp{{App: "web", Compute: provider.ComputeServerless, Workers: []string{"media"}}}; !reflect.DeepEqual(retirements[0].Apps, want) {
		t.Errorf("the deploy retired with live apps %+v, want %+v, so a provider can retire the workers a live app stopped hosting", retirements[0].Apps, want)
	}
}

func TestADryRunPlansTheRetirementOfAnAppItNoLongerDeploysAndRetiresNothing(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	if result, _ := deploy(t, client, twoAppRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() of web and admin = %q", result.GetError())
	}
	req := deployRequest()
	req.Dry = true

	result, events := deploy(t, client, req)

	if !result.GetSuccess() {
		t.Fatalf("Deploy(dry) = %q", result.GetError())
	}
	if names := groupNames(lastPlan(events)); !slices.Contains(names, fake.AppGroupKind+":admin") {
		t.Errorf("the dry run plans %v, want admin's retirement among them", names)
	}
	if got := vendor.Retirements(); len(got) != 1 {
		t.Errorf("the deploys retired %v, want only the real deploy's retirement", got)
	}
}

func TestAFailedDeployRetiresNothing(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	vendor.FakeStacks().Entering(func(spec provider.StackSpec) error {
		if spec.App != nil {
			return errors.New("the app stack fails")
		}
		return nil
	})

	if result, _ := deploy(t, client, deployRequest()); result.GetSuccess() {
		t.Fatal("Deploy() succeeded, want the failing app stack to fail it")
	}

	if got := vendor.Retirements(); len(got) != 0 {
		t.Errorf("the failed deploy retired %v, want nothing retired while the previous promotion still serves", got)
	}
}

type refusingRetirement struct{ *fake.Provider }

func (r refusingRetirement) Hooks() provider.Hooks {
	hooks := r.Provider.Hooks()
	hooks.Retirement = &provider.RetirementHooks{
		Plan: r.PlanRetirement,
		Retire: func(context.Context, provider.LiveApps, progress.Log) error {
			return errors.New("the workers stack is locked")
		},
	}
	return hooks
}

func TestADeployWhoseRetirementFailsServesAndWarns(t *testing.T) {
	builtProject(t)
	client := servedBy(t, refusingRetirement{fake.NewProvider(fake.Options{})})

	result, events := deploy(t, client, deployRequest())

	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want the promotion to serve though retiring failed", result.GetError())
	}
	if !slices.ContainsFunc(events, func(event *progressv1.OperationEvent) bool {
		return event.GetLevel() == progressv1.Level_LEVEL_WARN && strings.Contains(event.GetMessage(), "the workers stack is locked")
	}) {
		t.Error("the deploy did not warn that retiring failed, so what it left running goes unreported")
	}
}

func TestRemovingAPreviewRetiresEveryAppDeployedToIt(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	if result, _ := deploy(t, client, previewRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	if removed := removePersistentPr7(t, client); !removed.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %q", removed.GetError())
	}

	retirements := vendor.Retirements()
	if len(retirements) == 0 {
		t.Fatal("removing pr-7 retired nothing, want every app it deployed retired")
	}
	if last := retirements[len(retirements)-1]; last.Env != "pr-7" || len(last.Apps) != 0 {
		t.Errorf("removing pr-7 retired %+v, want pr-7 with no app live", last)
	}
}

func TestRemovingAProjectPlansAndRetiresEveryAppOfEachEnvironment(t *testing.T) {
	client, vendor := deployedProject(t)

	plan, err := client.PlanRemoveProject(context.Background(), projectRequest())
	if err != nil {
		t.Fatalf("PlanRemoveProject() error = %v", err)
	}
	if names := groupNames(plan); !slices.Contains(names, fake.AppGroupKind+":fake/web") {
		t.Errorf("the plan names %v, want web's retirement among them", names)
	}
	if result := removeProject(t, client, projectRequest()); !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q", result.GetError())
	}

	retirements := vendor.Retirements()
	if len(retirements) == 0 {
		t.Fatal("removing the project retired nothing")
	}
	if last := retirements[len(retirements)-1]; last.Tier != environment.TierProduction || len(last.Apps) != 0 {
		t.Errorf("removing the project retired %+v, want production with no app live", last)
	}
}
