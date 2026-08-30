package providerkit_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

const pushedCoordinate = "ghcr.io/acme/web:sha256-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func registryDeployRequest() *contractv1.DeployRequest {
	req := containerDeployRequest("/")
	req.ImageRegistry = &contractv1.ImageRegistry{
		Server:    "ghcr.io",
		Namespace: "acme",
		Username:  "acme-bot",
		Password:  "hunter2",
	}
	return req
}

func imageRows(plan *planv1.ChangePlan) []*planv1.Change {
	var rows []*planv1.Change
	for _, group := range plan.GetGroups() {
		for _, change := range group.GetChanges() {
			if change.GetKind() == providerkit.ImageKind {
				rows = append(rows, change)
			}
		}
	}
	return rows
}

func TestADryDeployShowsTheImagePushAsARowAndPushesNothing(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

	req := registryDeployRequest()
	req.Dry = true
	result, events := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	rows := imageRows(lastPlan(events))
	if len(rows) != 1 || rows[0].GetName() != "web" {
		t.Fatalf("the plan shows %d image rows, want the one push this deploy makes", len(rows))
	}
	if got := rows[0].GetAction(); got != planv1.Change_ACTION_CREATE {
		t.Errorf("the image row says %q, want %q for a digest the registry does not hold", got, providerkit.ActionCreate)
	}
	registry := provider.Registry()
	if len(registry.Asked()) == 0 {
		t.Error("the plan drew an image row without asking the registry anything, so the row is guesswork rather than a diff")
	}
	if pushed := registry.Pushed(); len(pushed) != 0 {
		t.Errorf("a dry deploy pushed %v; preview diffs on the digest and pushes nothing", pushed)
	}
}

type muteReleaser struct{}

func (muteReleaser) Plan(context.Context, providerkit.StackPlan, providerkit.Reporter) (providerkit.Plan, error) {
	return providerkit.Plan{}, nil
}

func (muteReleaser) PlanDestroy(_ context.Context, ref providerkit.StackRef, _ providerkit.Reporter) (providerkit.Plan, error) {
	return providerkit.Plan{}, nil
}

func (muteReleaser) Provision(_ context.Context, plan providerkit.StackPlan, _ providerkit.Reporter) (providerkit.StackResult, error) {
	return providerkit.StackResult{Containers: fake.StoodUpContainers(plan)}, nil
}

func (muteReleaser) Destroy(context.Context, providerkit.StackRef, providerkit.Reporter) error {
	return nil
}

func TestTheImageRowIsTheReleasersOwnAndNothingElseInventsIt(t *testing.T) {
	builtProject(t)
	base := fake.NewProvider(fake.Options{Region: "nowhere"})
	client := servedBy(t, refusingReleaser{Provider: base, releaser: muteReleaser{}})
	standsBootstrapped(t, client)

	req := registryDeployRequest()
	req.Dry = true
	result, events := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if rows := imageRows(lastPlan(events)); len(rows) != 0 {
		t.Fatalf("the plan shows %d image rows over a releaser that declared none: a row nothing in the release program emitted cannot be verified by --dry or replayed", len(rows))
	}
}

func TestADeployPushesTheImageTheBuildProducedUnderTheRegistryCoordinate(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	pushed := provider.Registry().Pushed()
	if len(pushed) != 1 {
		t.Fatalf("the deploy pushed %v, want the one image its container app runs", pushed)
	}
	if pushed[0].Source != containerTestImage {
		t.Errorf("the push read %q from the local store, want the image the build produced, %q", pushed[0].Source, containerTestImage)
	}
	if pushed[0].Target != pushedCoordinate {
		t.Errorf("the push wrote %q, want %q", pushed[0].Target, pushedCoordinate)
	}
}

func TestADigestTheRegistryAlreadyHoldsIsNotPushedAgain(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	provider.Registry().Holds(pushedCoordinate)

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if pushed := provider.Registry().Pushed(); len(pushed) != 0 {
		t.Errorf("the deploy pushed %v that the registry already holds", pushed)
	}
}

func TestADigestTheRegistryAlreadyHoldsStandsOnThePlan(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	provider.Registry().Holds(pushedCoordinate)

	req := registryDeployRequest()
	req.Dry = true
	_, events := deploy(t, client, req)

	rows := imageRows(lastPlan(events))
	if len(rows) != 1 || rows[0].GetAction() != planv1.Change_ACTION_KEEP {
		t.Errorf("the plan shows %v for an image the registry already holds, want one %q row", rows, providerkit.ActionKeep)
	}
}

func TestADeployWithNoRegistryPushesNothingAndShowsNoImageRow(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

	req := containerDeployRequest("/")
	req.Dry = true
	result, events := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if rows := imageRows(lastPlan(events)); len(rows) != 0 {
		t.Errorf("the plan shows %d image rows where nothing named a registry", len(rows))
	}
	if opened := provider.Registry().Opened(); len(opened) != 0 {
		t.Errorf("a registry was opened as %v where the deploy named none", opened)
	}
}

func TestAServerlessDeployNamesNoImageToPush(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

	req := deployRequest()
	req.ImageRegistry = &contractv1.ImageRegistry{Server: "ghcr.io", Namespace: "acme", Password: "hunter2"}
	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if asked := provider.Registry().Asked(); len(asked) != 0 {
		t.Errorf("a serverless deploy asked the registry about %v, and it ships zips rather than images", asked)
	}
}

func TestARegistryThatCannotBeReachedStopsTheDeploy(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	provider.Registry().Refusing(errors.New("the token is not accepted"))

	result, _ := deploy(t, client, registryDeployRequest())
	if result != nil && result.GetSuccess() {
		t.Fatal("Deploy() succeeded over a registry that refused the push, so the release would promote an image nothing can pull")
	}
	if !strings.Contains(result.GetError(), "the token is not accepted") {
		t.Errorf("Deploy() = %q, want the registry's own reason", result.GetError())
	}
}

func TestThePasswordTheDeployCarriesReachesTheRegistryAndNothingElse(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

	result, events := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	opened := provider.Registry().Opened()
	if len(opened) != 1 || opened[0].Password != "hunter2" {
		t.Fatalf("the registry was opened as %v, want the one target the deploy resolved", opened)
	}
	for _, event := range events {
		if strings.Contains(event.String(), "hunter2") {
			t.Fatalf("the deploy stream carries the registry password: %s", event.String())
		}
	}
}

func TestAnImageRowRidesInsideTheAppsOwnStackGroup(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := registryDeployRequest()
	req.Dry = true
	_, events := deploy(t, client, req)

	for _, group := range lastPlan(events).GetGroups() {
		names := changeNames(group)
		if !slices.Contains(names, providerkit.ImageKind+":web") {
			continue
		}
		if group.GetKind() != providerkit.StackGroupKind || !strings.Contains(group.GetName(), "web") {
			t.Fatalf("the image row sits in the %s group %q, want the app's own stack, so an apply of that stack is what pushes it",
				group.GetKind(), group.GetName())
		}
		return
	}
	t.Fatal("no group carries the image row")
}

func TestTheImageStoreIsOpenedFromTheTargetTheDeployCarries(t *testing.T) {
	store := fake.NewImages()
	push := providerkit.ImagePush{App: "web", Source: containerTestImage, Target: pushedCoordinate}
	plan := providerkit.ImagePlan{Store: store, Pushes: []providerkit.ImagePush{push}}

	if err := plan.Ship(context.Background(), nil); err != nil {
		t.Fatalf("Ship() = %v", err)
	}
	if err := plan.Ship(context.Background(), nil); err != nil {
		t.Fatalf("Ship() = %v", err)
	}
	if pushed := store.Pushed(); len(pushed) != 1 {
		t.Errorf("Ship() pushed %d times, want the second run to find the digest already there", len(pushed))
	}
}
