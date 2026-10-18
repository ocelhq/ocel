package providerserver_test

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func plannedApp(t *testing.T, vendor *fake.Provider) *provider.AppSpec {
	t.Helper()
	for _, spec := range vendor.FakeStacks().Planned() {
		if spec.App != nil {
			return spec.App
		}
	}
	t.Fatal("the dry deploy planned no app")
	return nil
}

func TestADryDeployPlansAnAppWithTheGrantsOfTheResourcesItUses(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	req := deployRequest()
	req.Dry = true
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	grants := plannedApp(t, vendor).Grants
	if !slices.ContainsFunc(grants, func(grant provider.Binding) bool { return grant.Name == "orders" }) {
		t.Errorf("the planned app is granted %+v, want orders: web uses it, and the apply grants it", grants)
	}
}

func TestADryDeployWithNoGateRecordedPlansTheAppWithTheGateItsApplyCreates(t *testing.T) {
	client, vendor, req := directPreviewServed(t, nil)
	directEdge(vendor).Gates(true)
	directEdge(vendor).GatesAtOrigin(true)

	req = protecting(req)
	req.Dry = true
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if got := plannedApp(t, vendor).Gate; got == nil {
		t.Error("the planned app has no gate, want the gate its apply creates: the origin enforces it")
	}
}

func TestAProtectedDeployWithNoGateRecordedAppliesThePlanItsDryRunDrew(t *testing.T) {
	client, vendor, req := directPreviewServed(t, nil)
	directEdge(vendor).Gates(true)
	directEdge(vendor).GatesAtOrigin(true)

	result, _ := deploy(t, client, consentedPlan(t, client, protecting(req)))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() with the plan its dry run drew = %q, want it applied", result.GetError())
	}
	if got := provisionedApp(t, vendor).Gate; got == nil {
		t.Error("the applied app has no gate, want the one the deploy created")
	}
}
