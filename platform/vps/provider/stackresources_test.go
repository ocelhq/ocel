package vps_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

func recordedStackOn(t *testing.T, stack string, recorded stackrecords.Stack) (*box, provider.Stacks, records.Store, provider.StackRef) {
	t.Helper()
	name, err := naming.ParseStackName(stack)
	if err != nil {
		t.Fatal(err)
	}
	ref := provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: name}
	store := fake.NewKeyValues()
	if err := stackrecords.Write(context.Background(), store, ref.Tier, ref.Project, ref.Name, recorded); err != nil {
		t.Fatal(err)
	}
	machine := &box{}
	p := over(machine)
	p.Recording(store)
	return machine, p.Stacks(), store, ref
}

func recordWorker(t *testing.T, store records.Store, ref provider.StackRef, name string, worker boxapi.QueueWorker) {
	t.Helper()
	value, err := json.Marshal(worker)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(context.Background(), records.Entry{Key: boxapi.QueueWorkerKey(ref.Tier, ref.Project, ref.Name.Env, name), Value: value}); err != nil {
		t.Fatal(err)
	}
}

func declaring(t *testing.T, declared ...*contractv1.ManifestResource) []byte {
	t.Helper()
	encoded, err := proto.Marshal(&contractv1.Manifest{Resources: declared})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestStackResourcesNameTheContainerEachAppRuns(t *testing.T) {
	t.Parallel()

	machine, stacks, store, ref := recordedStackOn(t, "prod--web--r0a1b2c3d", stackrecords.Stack{
		Kind: provider.StackApp, App: "web",
		Containers: []provider.AppContainer{{Name: "web", Physical: "shop-prod-web-r0a1b2c3d"}},
	})
	recordWorker(t, store, ref, "mailer", boxapi.QueueWorker{App: "web", Stack: ref.Name.String(), Container: "shop-prod-web-mailer-r0a1b2c3d"})
	recordWorker(t, store, ref, "billing", boxapi.QueueWorker{App: "api", Stack: "prod--api--r9f8e7d6c", Container: "shop-prod-api-billing-r9f8e7d6c"})

	listed, err := stacks.Resources(context.Background(), provider.StackResources{Ref: ref})
	if err != nil {
		t.Fatalf("Resources() = %v", err)
	}
	want := []provider.PhysicalResource{
		{Kind: "docker:container", ID: "shop-prod-web-r0a1b2c3d", Location: "box.invalid"},
		{Kind: "docker:container", ID: "shop-prod-web-mailer-r0a1b2c3d", Location: "box.invalid"},
	}
	if !slices.Equal(listed, want) {
		t.Errorf("Resources() = %+v, want %+v: the app's container and the worker container this stack started, on the box", listed, want)
	}
	if ran := machine.commands(); len(ran) > 0 {
		t.Errorf("listing ran %q on the box, want the names read from records alone", ran)
	}
}

func TestStackResourcesNameTheContainerAndVolumeABindingRuns(t *testing.T) {
	t.Parallel()

	machine, stacks, _, ref := recordedStackOn(t, "prod--infra", stackrecords.Stack{
		Kind: provider.StackInfra,
		Bindings: []provider.Binding{
			{Type: provider.BindingPostgres, Name: "main", Resource: "main"},
			{Type: provider.BindingKV, Name: "cache", Resource: "cache"},
			{Type: provider.BindingBucket, Name: "uploads", Resource: "uploads"},
			{Type: provider.BindingBucket, Name: "avatars", Resource: "avatars"},
			{Type: provider.BindingTopic, Name: "orders", Resource: "orders"},
			{Type: provider.BindingTask, Name: "emails", Resource: "emails"},
			{Type: provider.BindingRealtime, Name: "app", Resource: "app"},
			{Type: provider.BindingRealtime, Name: "chat", Resource: "chat"},
		},
		Resources: declaring(t,
			&contractv1.ManifestResource{
				Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "main"},
				Config:   &contractv1.ManifestResource_Postgres{Postgres: &resourcesv1.PostgresConfig{Version: "16"}},
			},
			&contractv1.ManifestResource{
				Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_KV, Name: "cache"},
			},
		),
	})

	listed, err := stacks.Resources(context.Background(), provider.StackResources{Ref: ref})
	if err != nil {
		t.Fatalf("Resources() = %v", err)
	}
	at := func(kind, id string) provider.PhysicalResource {
		return provider.PhysicalResource{Kind: kind, ID: id, Location: "box.invalid"}
	}
	want := []provider.PhysicalResource{
		at("docker:container", "shop-prod-main-pg-8017d2"),
		at("docker:volume", "shop-prod-main-pg-8017d2-g16"),
		at("docker:container", "shop-prod-cache-kv-0478e1"),
		at("docker:volume", "shop-prod-cache-kv-0478e1-g9"),
		at("docker:container", "shop-prod-store-s3-bd55b6"),
		at("docker:volume", "shop-prod-store-s3-bd55b6"),
		at("docker:container", "shop-prod-ocel-queue-a12aaa"),
		at("docker:volume", "shop-prod-ocel-queue-a12aaa-g18"),
		at("docker:volume", "shop-prod-ocel-queue-a12aaa-sockets"),
		at("docker:container", "shop-prod-ocel-realtime-2630bf"),
	}
	if !slices.Equal(listed, want) {
		t.Errorf("Resources() = %+v\nwant %+v\neach binding's container and volume once, under the names its hook gives them, the postgres volume under the version it declared and the kv volume under the default", listed, want)
	}
	if ran := machine.commands(); len(ran) > 0 {
		t.Errorf("listing ran %q on the box, want the names read from records alone", ran)
	}
}
