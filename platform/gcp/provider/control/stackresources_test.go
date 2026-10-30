package control

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/postgres"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

func listingProvider() *Provider {
	return &Provider{
		Options:   Options{Project: googletest.Project, Region: googletest.Region},
		Namespace: googletest.Names.Namespace(),
		Clients:   googletest.Clients("http://127.0.0.1:1"),
		Records:   fake.NewKeyValues(),
	}
}

var listedRef = provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack("prod")}

func listStackResources(t *testing.T, p *Provider, recorded provider.StackResult) []provider.PhysicalResource {
	t.Helper()
	listed, err := p.ListStackResources(t.Context(), listedRef, recorded)
	if err != nil {
		t.Fatalf("ListResources() = %v", err)
	}
	return listed
}

func TestStackResourcesNameTheCloudRunServiceEachAppRuns(t *testing.T) {
	t.Parallel()

	listed := listStackResources(t, listingProvider(), provider.StackResult{
		Functions:  []provider.Function{{Name: "web", Physical: "ocel-shop-prod-web-1a2b3c"}},
		Containers: []provider.AppContainer{{Name: "api", Physical: "ocel-shop-prod-api-4d5e6f"}},
	})

	want := []provider.PhysicalResource{
		{Kind: "run:service", ID: "ocel-shop-prod-web-1a2b3c", Location: googletest.Region},
		{Kind: "run:service", ID: "ocel-shop-prod-api-4d5e6f", Location: googletest.Region},
	}
	if !slices.Equal(listed, want) {
		t.Errorf("ListResources() = %+v, want %+v", listed, want)
	}
}

func TestStackResourcesNameTheDatabaseABindingProvisioned(t *testing.T) {
	t.Parallel()
	p := listingProvider()
	instance := must(googletest.Names.PostgresInstance("shop", "prod", "db", "a1b2c3"))
	key := records.Partition{Tier: listedRef.Tier, Root: postgres.RootPostgres}.Key("shop", "prod", "db")
	if _, err := p.Records.Write(t.Context(), records.Entry{Key: key, Value: []byte(`{"sealed":"c2VhbGVk","setOn":"` + instance + `"}`)}); err != nil {
		t.Fatal(err)
	}

	listed := listStackResources(t, p, provider.StackResult{Bindings: []provider.Binding{{Type: provider.BindingPostgres, Name: "db"}}})

	want := []provider.PhysicalResource{{Kind: "sqladmin:instance", ID: instance, Location: googletest.Region}}
	if !slices.Equal(listed, want) {
		t.Errorf("ListResources() = %+v, want %+v: the instance's name ends in a suffix minted at creation, so it is read from the password record set on it", listed, want)
	}
}

func TestStackResourcesNameNoDatabaseForAPostgresBindingWhosePasswordWasNeverSet(t *testing.T) {
	t.Parallel()

	listed := listStackResources(t, listingProvider(), provider.StackResult{Bindings: []provider.Binding{{Type: provider.BindingPostgres, Name: "db"}}})

	if len(listed) != 0 {
		t.Errorf("ListResources() = %+v, want nothing: no record names the instance", listed)
	}
}

func TestStackResourcesNameTheBucketABindingProvisioned(t *testing.T) {
	t.Parallel()
	bucket := must(googletest.Names.AppBucket("shop", "prod", "uploads", "a1b2c3"))

	listed := listStackResources(t, listingProvider(), provider.StackResult{Bindings: []provider.Binding{
		{Type: provider.BindingBucket, Name: "uploads", Properties: map[string]string{provider.PropertyBucket: bucket}},
	}})

	if len(listed) != 1 || listed[0].Kind != "storage:bucket" || listed[0].ID != bucket || listed[0].Location != googletest.Region {
		t.Errorf("ListResources() = %+v, want the Cloud Storage bucket %s in %s", listed, bucket, googletest.Region)
	}
}

func TestStackResourcesLinkABucketToItsConsolePage(t *testing.T) {
	t.Parallel()
	bucket := must(googletest.Names.AppBucket("shop", "prod", "uploads", "a1b2c3"))

	listed := listStackResources(t, listingProvider(), provider.StackResult{Bindings: []provider.Binding{
		{Type: provider.BindingBucket, Name: "uploads", Properties: map[string]string{provider.PropertyBucket: bucket}},
	}})

	want := "https://console.cloud.google.com/storage/browser/" + bucket
	if len(listed) != 1 || listed[0].ConsoleURL != want {
		t.Errorf("ListResources() = %+v, want the console link %s", listed, want)
	}
}

func TestStackResourcesNameTheMemorystoreInstanceAKVBindingProvisioned(t *testing.T) {
	t.Parallel()
	instance, err := googletest.Names.KVInstance("shop", "prod", "cache")
	if err != nil {
		t.Fatal(err)
	}

	listed := listStackResources(t, listingProvider(), provider.StackResult{Bindings: []provider.Binding{{Type: provider.BindingKV, Name: "cache"}}})

	want := []provider.PhysicalResource{{Kind: "memorystore:instance", ID: instance, Location: googletest.Region}}
	if !slices.Equal(listed, want) {
		t.Errorf("ListResources() = %+v, want %+v", listed, want)
	}
}

func TestStackResourcesNameTheTopicItsSubscriptionsAndItsScheduleATopicBindingProvisioned(t *testing.T) {
	t.Parallel()
	spec, err := json.Marshal(provider.TopicSpec{Cron: "0 * * * *", Consumers: []provider.ConsumerSpec{{Name: "mailer", Worker: "jobs"}}})
	if err != nil {
		t.Fatal(err)
	}
	scope := topics.Names{Namespace: googletest.Names.Namespace(), Scope: topics.Scope{Slug: "shop", Tier: listedRef.Tier, Environment: "prod"}}

	listed := listStackResources(t, listingProvider(), provider.StackResult{Bindings: []provider.Binding{{
		Type: provider.BindingTopic, Name: "orders",
		Properties: map[string]string{stackrecords.PropertyDeclared: "orders", stackrecords.PropertyTopicSpec: string(spec)},
	}}})

	want := []provider.PhysicalResource{
		{Kind: "pubsub:topic", ID: must(scope.Topic("orders")), Location: "global"},
		{Kind: "pubsub:subscription", ID: must(scope.Subscription("orders", "mailer")), Location: "global"},
		{Kind: "cloudscheduler:job", ID: must(scope.Topic("orders")), Location: googletest.Region},
	}
	if !slices.Equal(listed, want) {
		t.Errorf("ListResources() = %+v, want %+v", listed, want)
	}
}

func TestStackResourcesNameTheTopicATaskBindingProvisioned(t *testing.T) {
	t.Parallel()
	spec, err := json.Marshal(provider.TopicSpec{})
	if err != nil {
		t.Fatal(err)
	}
	scope := topics.Names{Namespace: googletest.Names.Namespace(), Scope: topics.Scope{Slug: "shop", Tier: listedRef.Tier, Environment: "prod"}}

	listed := listStackResources(t, listingProvider(), provider.StackResult{Bindings: []provider.Binding{{
		Type: provider.BindingTask, Name: "resize",
		Properties: map[string]string{stackrecords.PropertyDeclared: "resize", stackrecords.PropertyTopicSpec: string(spec)},
	}}})

	want := []provider.PhysicalResource{{Kind: "pubsub:topic", ID: must(scope.Topic("resize")), Location: "global"}}
	if !slices.Equal(listed, want) {
		t.Errorf("ListResources() = %+v, want %+v", listed, want)
	}
}

func TestStackResourcesNameTheGatewayTopicAndSecretsRealtimeBindingsProvisionedOnce(t *testing.T) {
	t.Parallel()
	named := googletest.Names
	gateway := must(named.RealtimeGateway("shop", "prod"))

	listed := listStackResources(t, listingProvider(), provider.StackResult{Bindings: []provider.Binding{
		{Type: provider.BindingRealtime, Name: "chat"},
		{Type: provider.BindingRealtime, Name: "presence", Resource: "live"},
	}})

	want := []provider.PhysicalResource{
		{Kind: "secretmanager:secret", ID: must(named.RealtimeSigningSecret("shop", "prod", "chat")), Location: "global"},
		{Kind: "run:service", ID: gateway, Location: googletest.Region},
		{Kind: "pubsub:topic", ID: gateway, Location: "global"},
		{Kind: "secretmanager:secret", ID: must(named.RealtimeKeysSecret(listedRef.Tier, "shop", "prod")), Location: "global"},
		{Kind: "secretmanager:secret", ID: must(named.RealtimeSigningSecret("shop", "prod", "live")), Location: "global"},
	}
	if !slices.Equal(listed, want) {
		t.Errorf("ListResources() = %+v, want %+v: every realtime of an environment shares one gateway, its topic and its keys", listed, want)
	}
}

func TestAnEndpointReadsAStacksResourcesFromItsRecordWithNoProvisioningEngine(t *testing.T) {
	t.Parallel()

	p := listingProvider()
	recorded := stackrecords.Stack{Kind: provider.StackApp, Functions: []provider.Function{{Name: "web", Physical: "ocel-shop-prod-web"}}}
	if err := stackrecords.Write(t.Context(), p.Records, listedRef.Tier, listedRef.Project, listedRef.Name, recorded); err != nil {
		t.Fatalf("Write() = %v", err)
	}

	listed, err := p.Stacks().Resources(t.Context(), provider.StackResources{Ref: listedRef})
	if err != nil {
		t.Fatalf("Stacks().Resources() = %v, want the recorded stack's resources: they are read from its record, which needs no engine", err)
	}
	want := []provider.PhysicalResource{{Kind: string(bootstrap.KindService), ID: "ocel-shop-prod-web", Location: googletest.Region}}
	if !slices.Equal(listed, want) {
		t.Errorf("Stacks().Resources() = %v, want %v", listed, want)
	}
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
