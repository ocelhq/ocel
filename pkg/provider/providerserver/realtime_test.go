package providerserver_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func realtimeResource(name string) *contractv1.ManifestResource {
	return &contractv1.ManifestResource{
		LogicalName: "realtime--" + name,
		Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME, Name: name},
		Config: &contractv1.ManifestResource_Realtime{Realtime: &resourcesv1.RealtimeConfig{Channels: []*resourcesv1.RealtimeChannel{{
			Pattern:   "orders/:orderId",
			Subscribe: resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_RULE,
			Publish:   resourcesv1.RealtimePublish_REALTIME_PUBLISH_SERVER,
		}}}},
	}
}

func TestAProviderThatProvisionsNoRealtimeRefusesItAsUnsupported(t *testing.T) {
	t.Parallel()

	facts := fake.NewProvider(fake.Options{}).Facts()
	manifest := &contractv1.Manifest{Slug: "shop", Resources: []*contractv1.ManifestResource{realtimeResource("app")}}
	err := providerserver.RefuseUnsupportedRealtime(facts, manifest)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeUnsupported {
		t.Fatalf("RefuseUnsupportedRealtime() = %v, want a refusal with code %s", err, refusal.CodeUnsupported)
	}
	for _, said := range []string{`realtime "app"`, "unsupported", string(facts.Vendor)} {
		if !strings.Contains(err.Error(), said) {
			t.Errorf("RefuseUnsupportedRealtime() = %q, want it to say %q", err, said)
		}
	}
}

func TestAProviderThatProvisionsRealtimeIsNotRefusedIt(t *testing.T) {
	t.Parallel()

	facts := fake.NewProvider(fake.Options{}).Facts()
	facts.Bindings = append(facts.Bindings, provider.BindingRealtime)
	manifest := &contractv1.Manifest{Slug: "shop", Resources: []*contractv1.ManifestResource{realtimeResource("app")}}
	if err := providerserver.RefuseUnsupportedRealtime(facts, manifest); err != nil {
		t.Errorf("RefuseUnsupportedRealtime() = %v, want nil from a provider whose bindings include realtime", err)
	}
}

func TestAManifestDeclaringNoRealtimeIsNeverRefusedForIt(t *testing.T) {
	t.Parallel()

	facts := fake.NewProvider(fake.Options{}).Facts()
	if err := providerserver.RefuseUnsupportedRealtime(facts, deployRequest().GetManifest()); err != nil {
		t.Errorf("RefuseUnsupportedRealtime() = %v, want nil for a manifest with no realtime resource", err)
	}
}

func TestADeployDeclaringRealtimeIsRefusedAtPreflightBeforeAnythingUploads(t *testing.T) {
	builtProject(t)
	vendor := &preflighting{Provider: fake.NewProvider(fake.Options{})}
	client := servedBy(t, vendor)

	req := deployRequest()
	req.Manifest.Resources = append(req.Manifest.Resources, realtimeResource("app"))
	stream, err := client.Deploy(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var failure string
	for stream.Receive() {
		if result := stream.Msg().GetResult(); result != nil {
			failure = result.GetError()
		}
	}
	said := failure + connectMessage(stream.Err())
	stream.Close()

	if !strings.Contains(said, "unsupported") || !strings.Contains(said, `realtime "app"`) {
		t.Fatalf("Deploy() said %q, want realtime app refused as unsupported", said)
	}
	if uploaded := vendor.uploads(); len(uploaded) != 0 {
		t.Errorf("the deploy uploaded %v before refusing, want nothing put in the store", uploaded)
	}
	if ran := vendor.Preflighted(); len(ran) != 0 {
		t.Errorf("the vendor's own preflight ran %d times, want the refusal to come first", len(ran))
	}
}
