package providerserver_test

import (
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func previewOf(lifecycle environmentv1.Lifecycle) *contractv1.DeployRequest {
	req := previewDeployRequest()
	req.Environment.Lifecycle = lifecycle
	if lifecycle == environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL {
		req.Manifest.Resources = nil
		req.Manifest.Usages = nil
	}
	return req
}

func previewServed(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})
	return client, vendor
}

func deployRefusal(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.DeployRequest) string {
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
			client, _ := previewServed(t)
			if result, _ := deploy(t, client, previewOf(lifecycle)); !result.GetSuccess() {
				t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
			}

			if listed := listedLifecycles(t, client)["pr-7"]; listed != lifecycle {
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
			client, vendor := previewServed(t)
			if result, _ := deploy(t, client, previewOf(created)); !result.GetSuccess() {
				t.Fatalf("Deploy() = %q, want the first deploy to create pr-7", result.GetError())
			}
			provisioned := len(vendor.FakeStacks().Provisioned())

			said := deployRefusal(t, client, previewOf(other))

			if !strings.Contains(said, "pr-7") || !strings.Contains(said, "created") {
				t.Errorf("Deploy() said %q, want it to name pr-7 and the lifecycle it was created with", said)
			}
			if got := len(vendor.FakeStacks().Provisioned()); got != provisioned {
				t.Errorf("the refused deploy provisioned %d stacks, want none", got-provisioned)
			}
			if listed := listedLifecycles(t, client)["pr-7"]; listed != created {
				t.Errorf("pr-7 is listed %s, want %s: a refused deploy changes nothing", listed, created)
			}
		})
	}
}
