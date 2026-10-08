package providerserver_test

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

func newPreviewRequest(lifecycle environmentv1.Lifecycle) *contractv1.DeployRequest {
	req := previewDeployRequest()
	req.Environment.Lifecycle = lifecycle
	if lifecycle == environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL {
		req.Manifest.Resources = nil
		req.Manifest.Usages = nil
	}
	return req
}

func servePreview(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})
	return client, vendor
}

func expectDeployRefused(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.DeployRequest) string {
	t.Helper()
	result, _, err := deployStream(t, client, req)
	if result.GetSuccess() {
		t.Fatal("Deploy() succeeded, want it refused")
	}
	return result.GetError() + connectMessage(err)
}

func TestAPreviewDeployRecordsTheLifecycleItCreatedThePreviewWith(t *testing.T) {
	for _, lifecycle := range []environmentv1.Lifecycle{
		environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
		environmentv1.Lifecycle_LIFECYCLE_PERSISTENT,
	} {
		t.Run(lifecycle.String(), func(t *testing.T) {
			client, _ := servePreview(t)
			if result, _ := deploy(t, client, newPreviewRequest(lifecycle)); !result.GetSuccess() {
				t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
			}

			if listed := listLifecycles(t, client)["pr-7"]; listed != lifecycle {
				t.Errorf("pr-7 is listed %s, want %s: the lifecycle it was created with", listed, lifecycle)
			}
		})
	}
}

func TestADeployOfTheOtherLifecycleToAPreviewIsRefusedBeforeItProvisionsAnything(t *testing.T) {
	for _, order := range [][2]environmentv1.Lifecycle{
		{environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL, environmentv1.Lifecycle_LIFECYCLE_PERSISTENT},
		{environmentv1.Lifecycle_LIFECYCLE_PERSISTENT, environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL},
	} {
		created, other := order[0], order[1]
		t.Run(created.String(), func(t *testing.T) {
			client, vendor := servePreview(t)
			if result, _ := deploy(t, client, newPreviewRequest(created)); !result.GetSuccess() {
				t.Fatalf("Deploy() = %q, want the first deploy to create pr-7", result.GetError())
			}
			provisioned := len(vendor.FakeStacks().Provisioned())

			said := expectDeployRefused(t, client, newPreviewRequest(other))

			if !strings.Contains(said, "pr-7") || !strings.Contains(said, "created") {
				t.Errorf("Deploy() said %q, want it to name pr-7 and the lifecycle it was created with", said)
			}
			if !strings.Contains(said, "`ocel preview rm pr-7`") {
				t.Errorf("Deploy() said %q, want it to name the command that removes pr-7 and no other preview", said)
			}
			redeploy := map[environmentv1.Lifecycle]string{
				environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL:  "deploy it without --persistent",
				environmentv1.Lifecycle_LIFECYCLE_PERSISTENT: "deploy it with --persistent",
			}[created]
			if !strings.Contains(said, redeploy) {
				t.Errorf("Deploy() said %q, want it to say %q", said, redeploy)
			}
			if got := len(vendor.FakeStacks().Provisioned()); got != provisioned {
				t.Errorf("the refused deploy provisioned %d stacks, want none", got-provisioned)
			}
			if listed := listLifecycles(t, client)["pr-7"]; listed != created {
				t.Errorf("pr-7 is listed %s, want %s: a refused deploy changes nothing", listed, created)
			}
		})
	}
}

func newEphemeralRequestWithOrders() *contractv1.DeployRequest {
	req := previewDeployRequest()
	req.Environment.Lifecycle = environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL
	return req
}

func publishPreviewRecord(t *testing.T, vendor *fake.Provider, preview string, binding *bindingsv1.Binding) {
	t.Helper()
	pair, err := variablestoreserver.BindingPair("terraform", binding)
	if err != nil {
		t.Fatal(err)
	}
	store := variablestore.Store{KeyValues: vendor.KeyValues(), Cipher: vendor.Cipher()}
	scope := variablestore.Scope{Project: "shop", Tier: environment.TierPreview}
	if _, err := store.SetBindings(t.Context(), scope, preview, "terraform",
		[]variablestore.NamedBindingWrite{{Name: binding.GetName(), Write: pair}}); err != nil {
		t.Fatal(err)
	}
}

func TestAnEphemeralDeployOfAResourceNoBindingCoversIsRefused(t *testing.T) {
	client, vendor := servePreview(t)

	said := expectDeployRefused(t, client, newEphemeralRequestWithOrders())

	for _, want := range []string{"orders", "--persistent", "`ocel bindings set --preview --environment pr-7`", "`ocel bindings set --preview`"} {
		if !strings.Contains(said, want) {
			t.Errorf("Deploy() said %q, want it to contain %q", said, want)
		}
	}
	if provisioned := vendor.FakeStacks().Provisioned(); len(provisioned) != 0 {
		t.Errorf("the refused deploy provisioned %d stacks, want none", len(provisioned))
	}
}

func TestAnEphemeralDeployOfAResourceABindingCoversGrantsThatBinding(t *testing.T) {
	for name, preview := range map[string]string{"at the preview": "pr-7", "tier-wide": ""} {
		t.Run(name, func(t *testing.T) {
			client, vendor := servePreview(t)
			publishPreviewRecord(t, vendor, preview, postgresRecord("orders", "terraform"))

			result, _ := deploy(t, client, newEphemeralRequestWithOrders())

			if !result.GetSuccess() {
				t.Fatalf("Deploy() = %q, want the published orders to cover the resource", result.GetError())
			}
			provisioned := vendor.FakeStacks().Provisioned()
			if len(provisioned) != 1 || provisioned[0].Kind != provider.StackApp {
				t.Fatalf("the stacks port saw %d specs, want only the app stack: an ephemeral preview has no infra stack", len(provisioned))
			}
			if grants := grantNames(provisioned[0]); !slices.Equal(grants, []string{"orders"}) {
				t.Errorf("web was granted %v, want the published orders", grants)
			}
		})
	}
}

func TestAPersistentPreviewWhoseFirstDeployFailsAfterItsInfraKeepsItsLifecycle(t *testing.T) {
	client, vendor := servePreview(t)
	vendor.FakeStacks().Entering(func(spec provider.StackSpec) error {
		if spec.Kind == provider.StackApp {
			return errors.New("the app stack failed")
		}
		return nil
	})
	if result, _ := deploy(t, client, newPreviewRequest(environmentv1.Lifecycle_LIFECYCLE_PERSISTENT)); result.GetSuccess() {
		t.Fatal("Deploy() succeeded, want the app stack to fail it after the infra stack")
	}
	vendor.FakeStacks().Entering(nil)
	provisioned := len(vendor.FakeStacks().Provisioned())

	said := expectDeployRefused(t, client, newPreviewRequest(environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL))

	if !strings.Contains(said, "created persistent") {
		t.Errorf("Deploy() said %q, want it refused because pr-7 was created persistent", said)
	}
	if got := len(vendor.FakeStacks().Provisioned()); got != provisioned {
		t.Errorf("the ephemeral deploy provisioned %d stacks, want none: pr-7's infra stack would be orphaned", got-provisioned)
	}
}

func TestAFirstDeployOfAPreviewRefusedBeforeItProvisionsLeavesTheNameFreeForTheOtherLifecycle(t *testing.T) {
	client, _ := servePreview(t)
	expectDeployRefused(t, client, newEphemeralRequestWithOrders())

	if result, _ := deploy(t, client, newPreviewRequest(environmentv1.Lifecycle_LIFECYCLE_PERSISTENT)); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want the persistent deploy to create pr-7: the refused ephemeral deploy provisioned nothing to claim it for", result.GetError())
	}

	if listed := listLifecycles(t, client)["pr-7"]; listed != environmentv1.Lifecycle_LIFECYCLE_PERSISTENT {
		t.Errorf("pr-7 is listed %s, want persistent", listed)
	}
}

func TestADeployRacingTheFirstDeployOfAPreviewIsRefusedBeforeItProvisions(t *testing.T) {
	client, vendor := servePreview(t)
	var raced sync.Once
	var said string
	var provisionedByRacer int
	vendor.FakeStacks().Entering(func(spec provider.StackSpec) error {
		if spec.Kind != provider.StackInfra {
			return nil
		}
		raced.Do(func() {
			before := len(vendor.FakeStacks().Provisioned())
			result, _, err := deployStream(t, client, newPreviewRequest(environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL))
			said = result.GetError() + connectMessage(err)
			provisionedByRacer = len(vendor.FakeStacks().Provisioned()) - before
		})
		return nil
	})

	if result, _ := deploy(t, client, newPreviewRequest(environmentv1.Lifecycle_LIFECYCLE_PERSISTENT)); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want the persistent deploy that claimed pr-7 first to succeed", result.GetError())
	}

	if !strings.Contains(said, "another deploy to pr-7 is running") {
		t.Errorf("the racing ephemeral Deploy() said %q, want it refused because the persistent deploy holds pr-7", said)
	}
	if provisionedByRacer != 0 {
		t.Errorf("the racing ephemeral deploy provisioned %d stacks, want none", provisionedByRacer)
	}
	if listed := listLifecycles(t, client)["pr-7"]; listed != environmentv1.Lifecycle_LIFECYCLE_PERSISTENT {
		t.Errorf("pr-7 is listed %s, want persistent", listed)
	}
}

func TestADeployOfTheOtherLifecycleToAPreviewIsRefusedBeforeItRepairsTheBootstrap(t *testing.T) {
	client, vendor := servePreview(t)
	if result, _ := deploy(t, client, newPreviewRequest(environmentv1.Lifecycle_LIFECYCLE_PERSISTENT)); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want the first deploy to create pr-7", result.GetError())
	}
	repairing := true
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:           environmentv1.Tier_TIER_PREVIEW,
		Features:       []string{fake.FeatureCache, fake.FeatureImages},
		RepairOnDeploy: &repairing,
	})
	vendor.FakeBootstrap().MarkStale(fake.FeatureCache, fake.FeatureImages)
	applied := len(vendor.FakeBootstrap().Applied())

	expectDeployRefused(t, client, newPreviewRequest(environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL))

	if got := len(vendor.FakeBootstrap().Applied()); got != applied {
		t.Errorf("the refused deploy applied the bootstrap %d times, want none: it is refused before it changes anything", got-applied)
	}
}
