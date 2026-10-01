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

func kvResource(name string) *contractv1.ManifestResource {
	return &contractv1.ManifestResource{
		LogicalName: "kv--" + name,
		Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_KV, Name: name},
		Config:      &contractv1.ManifestResource_Kv{Kv: &resourcesv1.KvConfig{}},
	}
}

func TestAProviderThatProvisionsNoKVStoreRefusesOneAsUnsupported(t *testing.T) {
	t.Parallel()

	facts := fake.NewProvider(fake.Options{}).Facts()
	manifest := &contractv1.Manifest{Slug: "shop", Resources: []*contractv1.ManifestResource{kvResource("cache")}}
	err := providerserver.RefuseUnsupportedKVStores(facts, manifest)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeUnsupported {
		t.Fatalf("RefuseUnsupportedKVStores() = %v, want a refusal with code %s", err, refusal.CodeUnsupported)
	}
	for _, said := range []string{`kv "cache"`, "unsupported", string(facts.Vendor)} {
		if !strings.Contains(err.Error(), said) {
			t.Errorf("RefuseUnsupportedKVStores() = %q, want it to say %q", err, said)
		}
	}
}

func TestAProviderThatProvisionsKVStoresIsNotRefusedOne(t *testing.T) {
	t.Parallel()

	facts := fake.NewProvider(fake.Options{}).Facts()
	facts.Bindings = append(facts.Bindings, provider.BindingKV)
	manifest := &contractv1.Manifest{Slug: "shop", Resources: []*contractv1.ManifestResource{kvResource("cache")}}
	if err := providerserver.RefuseUnsupportedKVStores(facts, manifest); err != nil {
		t.Errorf("RefuseUnsupportedKVStores() = %v, want nil from a provider whose bindings include kv", err)
	}
}

func TestAManifestDeclaringNoKVStoreIsNeverRefusedForOne(t *testing.T) {
	t.Parallel()

	facts := fake.NewProvider(fake.Options{}).Facts()
	if err := providerserver.RefuseUnsupportedKVStores(facts, deployRequest().GetManifest()); err != nil {
		t.Errorf("RefuseUnsupportedKVStores() = %v, want nil for a manifest with no kv store", err)
	}
}

func TestADeployDeclaringAKVStoreIsRefusedAtPreflightBeforeAnythingUploads(t *testing.T) {
	builtProject(t)
	vendor := &preflighting{Provider: fake.NewProvider(fake.Options{})}
	client := servedBy(t, vendor)

	req := deployRequest()
	req.Manifest.Resources = append(req.Manifest.Resources, kvResource("cache"))
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

	if !strings.Contains(said, "unsupported") || !strings.Contains(said, `kv "cache"`) {
		t.Fatalf("Deploy() said %q, want kv store cache refused as unsupported", said)
	}
	if uploaded := vendor.uploads(); len(uploaded) != 0 {
		t.Errorf("the deploy uploaded %v before refusing, want nothing put in the store", uploaded)
	}
	if ran := vendor.Preflighted(); len(ran) != 0 {
		t.Errorf("the vendor's own preflight ran %d times, want the refusal to come first", len(ran))
	}
}
