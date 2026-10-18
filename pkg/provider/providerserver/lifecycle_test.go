package providerserver_test

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

func newPreviewRequest(lifecycle environmentv1.Lifecycle) *contractv1.DeployRequest {
	req := previewDeployRequest()
	req.Environment.Lifecycle = lifecycle
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

func publishPreviewRecord(t *testing.T, vendor *fake.Provider, preview string, binding *bindingsv1.Binding) {
	t.Helper()
	pair, err := variablestoreserver.BindingPair("terraform", &variablestorev1.PublishedBinding{Binding: binding})
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

func TestAnEphemeralDeployProvisionsTheResourcesItDeclaresInAnInfraStackOfItsOwn(t *testing.T) {
	client, vendor := servePreview(t)

	result, _ := deploy(t, client, newPreviewRequest(environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL))

	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want an ephemeral preview to provision orders itself", result.GetError())
	}
	var kinds []provider.StackKind
	var grants []string
	for _, spec := range vendor.FakeStacks().Provisioned() {
		kinds = append(kinds, spec.Kind)
		if spec.Kind == provider.StackApp {
			grants = grantNames(spec)
		}
	}
	if !slices.Contains(kinds, provider.StackInfra) {
		t.Errorf("the stacks port saw %v, want the preview's own infra stack beside its app stack", kinds)
	}
	if !slices.Equal(grants, []string{"orders"}) {
		t.Errorf("web was granted %v, want the orders the preview provisioned", grants)
	}
}

func TestAPreviewResourceNamedLikeAnotherPublishersRecordIsRefusedBeforeAnythingIsProvisioned(t *testing.T) {
	records := map[string]*bindingsv1.Binding{"of the same type": postgresRecord("orders", "terraform"), "of another type": bucketRecord("orders", "terraform")}
	for _, lifecycle := range []environmentv1.Lifecycle{environmentv1.Lifecycle_LIFECYCLE_PERSISTENT, environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL} {
		for at, preview := range map[string]string{"at the preview": "pr-7", "tier-wide": "", "at another preview": "pr-9"} {
			for kind, record := range records {
				for operation, refused := range map[string]func(*testing.T, contractv1connect.ProviderServiceClient, *contractv1.DeployRequest) string{
					"Deploy": expectDeployRefused,
					"ProvisionInfra": func(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.DeployRequest) string {
						return infraRefusal(t, client, infraRequest(req))
					},
				} {
					t.Run(strings.Join([]string{lifecycle.String(), at, kind, operation}, "/"), func(t *testing.T) {
						client, vendor := servePreview(t)
						publishPreviewRecord(t, vendor, preview, record)

						said := refused(t, client, newPreviewRequest(lifecycle))

						for _, want := range []string{`"orders"`, "publisher terraform", "rename"} {
							if !strings.Contains(said, want) {
								t.Errorf("%s() said %q, want it to contain %q", operation, said, want)
							}
						}
						if got := len(vendor.FakeStacks().Provisioned()); got != 0 {
							t.Errorf("the refused %s provisioned %d stacks, want none: publishing orders afterwards is refused, so its infra would be left behind", operation, got)
						}
					})
				}
			}
		}
	}
}

func TestARefusalOfAPreviewResourceNamedLikeAnotherPublishersRecordShowsTheBindingThatConsumesIt(t *testing.T) {
	client, vendor := servePreview(t)
	publishPreviewRecord(t, vendor, "", postgresRecord("orders", "terraform"))

	said := expectDeployRefused(t, client, newPreviewRequest(environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL))

	if want := `"bindings": { "postgres": { "orders": "@orders" } }`; !strings.Contains(said, want) {
		t.Errorf("Deploy() said %q, want it to show %s, the binding written as config accepts it", said, want)
	}
}

func TestAPreviewRedeploysOverTheRecordsItsOwnLastDeployPublished(t *testing.T) {
	for _, lifecycle := range []environmentv1.Lifecycle{environmentv1.Lifecycle_LIFECYCLE_PERSISTENT, environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL} {
		t.Run(lifecycle.String(), func(t *testing.T) {
			client, _ := servePreview(t)
			for deploys := range 2 {
				if result, _ := deploy(t, client, newPreviewRequest(lifecycle)); !result.GetSuccess() {
					t.Fatalf("deploy %d = %q, want it to succeed: orders is published by ocel itself", deploys+1, result.GetError())
				}
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

func TestRemovingAnEphemeralPreviewDestroysTheInfraStackItsDeployProvisioned(t *testing.T) {
	client, vendor := servePreview(t)
	if result, _ := deploy(t, client, newPreviewRequest(environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL)); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want the ephemeral preview deployed", result.GetError())
	}
	store := variablestore.Store{KeyValues: vendor.KeyValues(), Cipher: vendor.Cipher()}
	scope := variablestore.Scope{Project: "shop", Tier: environment.TierPreview}
	owned := func() int {
		records, err := store.ListBindings(t.Context(), scope, "pr-7")
		if err != nil {
			t.Fatal(err)
		}
		var n int
		for _, record := range records {
			if record.Environment == "pr-7" && record.Owner == variablestore.OwnerOcel {
				n++
			}
		}
		return n
	}
	if owned() == 0 {
		t.Fatal("the deploy published no binding of its own for pr-7, so there is nothing for the removal to take with it")
	}

	stream, err := client.RemoveEnvironment(t.Context(), &contractv1.RemoveEnvironmentRequest{
		Slug: "shop",
		Environment: &environmentv1.Environment{
			Tier:      environmentv1.Tier_TIER_PREVIEW,
			Identity:  "pr-7",
			Lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
		},
	})
	if err != nil {
		t.Fatalf("RemoveEnvironment() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %v, %v", result.GetError(), err)
	}

	if !slices.Contains(vendor.Journal(), "destroy "+naming.InfraStack("pr-7").String()) {
		t.Errorf("the removal reached %v, want pr-7's infra stack destroyed: it would bill on, orphaned", vendor.Journal())
	}
	if got := owned(); got != 0 {
		t.Errorf("pr-7 keeps %d bindings ocel wrote, want them removed with the preview", got)
	}
}
