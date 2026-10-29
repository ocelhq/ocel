package providerserver_test

import (
	"context"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/realtime"
)

type uninventoried struct{ provider.Provider }

func (u uninventoried) Hooks() provider.Hooks {
	hooks := u.Provider.Hooks()
	hooks.Inventory = nil
	return hooks
}

func inventoryServed(t *testing.T, p provider.Provider) contractv1connect.ProviderServiceClient {
	t.Helper()
	config := providerserver.Config{
		Version: "1.0.0",
		New:     func(context.Context, provider.Settings) (provider.Provider, error) { return p, nil },
	}
	server := httptest.NewServer(providerserver.ConformanceMux(config))
	t.Cleanup(server.Close)
	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := client.Configure(context.Background(), configureInWorkingDir(t)); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	return client
}

func inventoryRequest() *contractv1.InventoryRequest {
	req := twoAppRequest()
	return &contractv1.InventoryRequest{
		Manifest:    req.GetManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		Edge:        req.GetEdge(),
	}
}

func TestAProviderThatTakesNoInventorySaysSoOnInventory(t *testing.T) {
	client := inventoryServed(t, uninventoried{fake.NewProvider(fake.Options{Region: "nowhere"})})

	_, err := client.Inventory(context.Background(), inventoryRequest())
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("Inventory() error = %v, want Unimplemented", err)
	}
}

func TestInventoryHandsTheProviderTheDeployItWouldMake(t *testing.T) {
	client := inventoryServed(t, fake.NewProvider(fake.Options{Region: "nowhere"}))

	set, err := client.Inventory(context.Background(), inventoryRequest())
	if err != nil {
		t.Fatalf("Inventory() error = %v", err)
	}
	if set.GetSource() != "ocel" {
		t.Errorf("source = %q, want ocel", set.GetSource())
	}
	kinds := map[string]int{}
	for _, scope := range set.GetScopes() {
		kinds[scope.GetKind()]++
	}
	if kinds["project"] != 1 || kinds["environment"] != 1 || kinds["app"] != 2 {
		t.Errorf("scope kinds = %v, want one project, one environment, two apps", kinds)
	}
	for _, resource := range set.GetResources() {
		if resource.GetVendor() != "fake" || resource.GetRegion() != "nowhere" {
			t.Errorf("resource %s has vendor %q region %q", resource.GetId(), resource.GetVendor(), resource.GetRegion())
		}
	}
}

func countInventoryTypes(set *costv1.ResourceSet) map[string]int {
	types := map[string]int{}
	for _, resource := range set.GetResources() {
		types[resource.GetType()]++
	}
	return types
}

func TestInventoryOfAnEphemeralPreviewListsTheResourcesItProvisions(t *testing.T) {
	client := inventoryServed(t, fake.NewProvider(fake.Options{Region: "nowhere"}))
	req := inventoryRequest()
	req.Environment = &environmentv1.Environment{
		Tier:      environmentv1.Tier_TIER_PREVIEW,
		Lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
		Identity:  "preview",
	}

	set, err := client.Inventory(context.Background(), req)
	if err != nil {
		t.Fatalf("Inventory() error = %v", err)
	}
	types := countInventoryTypes(set)
	if types[fake.TypePostgres] == 0 || types[fake.TypeBucket] == 0 {
		t.Errorf("inventory types = %v, want the postgres and bucket an ephemeral preview provisions", types)
	}
	if types[fake.TypeFunction] != 2 {
		t.Errorf("inventory types = %v, want both apps' functions still listed", types)
	}
}

func TestInventoryOfAPersistentPreviewListsTheResourcesItProvisions(t *testing.T) {
	client := inventoryServed(t, fake.NewProvider(fake.Options{Region: "nowhere"}))
	req := inventoryRequest()
	req.Environment = &environmentv1.Environment{
		Tier:      environmentv1.Tier_TIER_PREVIEW,
		Lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT,
		Identity:  "staging",
	}

	set, err := client.Inventory(context.Background(), req)
	if err != nil {
		t.Fatalf("Inventory() error = %v", err)
	}
	if types := countInventoryTypes(set); types[fake.TypePostgres] != 1 || types[fake.TypeBucket] != 1 {
		t.Errorf("inventory types = %v, want the postgres and the bucket its infra stack provisions", types)
	}
}

type inventoryRecorded struct {
	provider.Provider
	asked *provider.InventoryRequest
}

func (s inventoryRecorded) Hooks() provider.Hooks {
	hooks := s.Provider.Hooks()
	inventory := hooks.Inventory
	hooks.Inventory = func(ctx context.Context, req provider.InventoryRequest) (*costv1.ResourceSet, error) {
		*s.asked = req
		return inventory(ctx, req)
	}
	return hooks
}

func TestInventoryHandsTheProviderEachConsumerScheduleAndWorkerItWouldRun(t *testing.T) {
	recorded := inventoryRecorded{Provider: fake.NewProvider(fake.Options{Region: "nowhere"}), asked: &provider.InventoryRequest{}}
	client := inventoryServed(t, recorded)
	req := inventoryRequest()
	req.Manifest.Workers = []*contractv1.ManifestWorker{{Name: "media", App: "admin", Compute: string(provider.ComputeServerless)}}
	req.Manifest.Resources = append(req.Manifest.Resources, &contractv1.ManifestResource{
		LogicalName: "task--nightly-report",
		Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, Name: "nightly-report"},
		Config: &contractv1.ManifestResource_Topic{Topic: &contractv1.ManifestTopic{
			Cron:      "0 3 * * *",
			Consumers: []*contractv1.ManifestConsumer{{Name: "nightly-report", Worker: "media", Exclusive: true}},
		}},
	})

	if _, err := client.Inventory(context.Background(), req); err != nil {
		t.Fatalf("Inventory() error = %v", err)
	}
	var task *provider.TopicSpec
	for _, resource := range recorded.asked.Resources {
		if resource.Type == provider.BindingTask {
			task = resource.Topic
		}
	}
	if task == nil || task.Cron != "0 3 * * *" || len(task.Consumers) != 1 || task.Consumers[0].Worker != "media" {
		t.Errorf("inventory task = %+v, want its schedule and its one consumer on worker media", task)
	}
	workers := map[string][]provider.WorkerSpec{}
	for _, app := range recorded.asked.Deploy.Apps {
		workers[app.App] = app.Workers
	}
	if want := []provider.WorkerSpec{{Name: "media", App: "admin", Compute: provider.ComputeServerless}}; !reflect.DeepEqual(workers["admin"], want) {
		t.Errorf("inventory admin workers = %+v, want %+v", workers["admin"], want)
	}
}

func TestInventoryHandsTheProviderEachRealtimeChannelAndTheTokenTTL(t *testing.T) {
	recorded := inventoryRecorded{Provider: fake.NewProvider(fake.Options{Region: "nowhere"}), asked: &provider.InventoryRequest{}}
	client := inventoryServed(t, recorded)
	req := inventoryRequest()
	req.Manifest.Resources = append(req.Manifest.Resources, &contractv1.ManifestResource{
		LogicalName: "realtime--app",
		Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME, Name: "app"},
		Config: &contractv1.ManifestResource_Realtime{Realtime: &resourcesv1.RealtimeConfig{
			TokenTtl: durationpb.New(2 * time.Minute),
			Channels: []*resourcesv1.RealtimeChannel{{
				Pattern:   "orders/:orderId",
				Subscribe: resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_PUBLIC,
				Publish:   resourcesv1.RealtimePublish_REALTIME_PUBLISH_SERVER,
			}},
		}},
	})

	if _, err := client.Inventory(context.Background(), req); err != nil {
		t.Fatalf("Inventory() error = %v", err)
	}
	var listed *provider.RealtimeSpec
	for _, resource := range recorded.asked.Resources {
		if resource.Type == provider.BindingRealtime {
			listed = resource.Realtime
		}
	}
	want := &provider.RealtimeSpec{
		TokenTTL: 2 * time.Minute,
		Channels: []provider.ChannelSpec{{Pattern: "orders/:orderId", Subscribe: realtime.ChannelAccessPublic, Publish: realtime.ChannelAccessServer}},
	}
	if !reflect.DeepEqual(listed, want) {
		t.Errorf("inventory realtime = %+v, want %+v", listed, want)
	}
}

func TestConfigureSaysWhetherTheProviderTakesInventory(t *testing.T) {
	for _, tc := range []struct {
		name   string
		served provider.Provider
		want   bool
	}{
		{"a provider with an inventory hook", fake.NewProvider(fake.Options{Region: "nowhere"}), true},
		{"a provider without one", uninventoried{fake.NewProvider(fake.Options{Region: "nowhere"})}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := providerserver.Config{
				Version: "1.0.0",
				New:     func(context.Context, provider.Settings) (provider.Provider, error) { return tc.served, nil },
			}
			server := httptest.NewServer(providerserver.ConformanceMux(config))
			t.Cleanup(server.Close)

			configured, err := contractv1connect.NewProviderServiceClient(server.Client(), server.URL).
				Configure(context.Background(), configureInWorkingDir(t))
			if err != nil {
				t.Fatalf("Configure() error = %v", err)
			}
			if got := configured.GetFacts().GetTakesInventory(); got != tc.want {
				t.Errorf("Configure() says takes_inventory = %t, want %t", got, tc.want)
			}
		})
	}
}
