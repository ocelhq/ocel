package kv

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func aKVStore(t *testing.T, spec provider.KVSpec) resources.ProvisionRequest {
	t.Helper()
	return resources.ProvisionRequest{
		Ref:      provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack("prod")},
		Resource: provider.Resource{Name: "cache", Type: provider.BindingKV, KV: &spec},
	}
}

func servingMemorystore(t *testing.T) (*KV, *googletest.Memorystore) {
	t.Helper()
	server, url := googletest.StartMemorystore(t)
	return New(googletest.Opener(googletest.Clients(url)), fake.NewKeyValues(), false), server
}

func storeInstance(t *testing.T) string {
	t.Helper()
	instance, err := googletest.Names.KVInstance("shop", "prod", "cache")
	if err != nil {
		t.Fatal(err)
	}
	return "projects/acme-prod/locations/europe-west1/instances/" + instance
}

type said struct {
	progress.Log
	lines []string
}

func (s *said) Say(line string) { s.lines = append(s.lines, line) }

func TestAStoreIsOneMemorystoreNodeWithAppendOnlyPersistenceAndTokenAuthOverTLS(t *testing.T) {
	p, server := servingMemorystore(t)

	binding, err := p.Provision(context.Background(), aKVStore(t, provider.KVSpec{Version: "9", MemoryBytes: 256 << 20}), nil)
	if err != nil {
		t.Fatalf("ProvisionKV() = %v", err)
	}

	created := server.Creates()
	if len(created) != 1 {
		t.Fatalf("Memorystore was asked to create %d instances, want one per store", len(created))
	}
	sent := created[0]
	if sent.Mode != "CLUSTER_DISABLED" || sent.ShardCount != 1 {
		t.Errorf("created mode %q with %d shards, want cluster mode off on one shard", sent.Mode, sent.ShardCount)
	}
	if sent.PersistenceConfig == nil || sent.PersistenceConfig.Mode != "AOF" || sent.PersistenceConfig.AOFConfig == nil ||
		sent.PersistenceConfig.AOFConfig.AppendFsync != "EVERY_SEC" {
		t.Errorf("created persistence %s, want the append-only file fsynced every second", googletest.JSON(t, sent.PersistenceConfig))
	}
	if sent.AuthorizationMode != "TOKEN_AUTH" || sent.TransitEncryptionMode != "SERVER_AUTHENTICATION" {
		t.Errorf("created authorization %q over %q, want token auth over TLS", sent.AuthorizationMode, sent.TransitEncryptionMode)
	}
	if sent.NodeType != "CUSTOM_PICO" || sent.EngineVersion != "VALKEY_9_1" {
		t.Errorf("created a %s node running %s, want the smallest node holding 256mb running Valkey 9.1", sent.NodeType, sent.EngineVersion)
	}
	if sent.EngineConfigs["maxmemory"] != "268435456" || sent.EngineConfigs["maxmemory-policy"] != "noeviction" {
		t.Errorf("created engine configs %v, want maxmemory at the declared memory and noeviction when no eviction is declared", sent.EngineConfigs)
	}
	if sent.Labels["ocel-tier"] != "production" || sent.Labels["ocel-namespace"] != "ocel" {
		t.Errorf("created labels %v, want the namespace and tier whose network the store is on", sent.Labels)
	}
	if sent.DeletionProtectionEnabled == nil || *sent.DeletionProtectionEnabled {
		t.Errorf("created deletion protection %v, want it off so a destroy can take the store down", sent.DeletionProtectionEnabled)
	}
	want := gcloud.PSCAutoConnection{ProjectID: "acme-prod", Network: "projects/acme-prod/global/networks/ocel-prod-network"}
	if len(sent.Endpoints) != 1 || len(sent.Endpoints[0].Connections) != 1 || sent.Endpoints[0].Connections[0].PSCAutoConnection == nil ||
		*sent.Endpoints[0].Connections[0].PSCAutoConnection != want {
		t.Errorf("created endpoints %s, want one automatic connection into the tier's network", googletest.JSON(t, sent.Endpoints))
	}

	if err := provider.VerifyProperties(binding); err != nil {
		t.Fatalf("VerifyProperties() = %v", err)
	}
	properties := binding.Properties
	if binding.Name != "cache" || binding.Type != provider.BindingKV {
		t.Errorf("binding = %s %s, want kv cache", binding.Type, binding.Name)
	}
	if properties[provider.PropertyHost] != googletest.StoreAddress || properties[provider.PropertyPort] != "6379" {
		t.Errorf("binding reaches %s:%s, want the instance's primary endpoint", properties[provider.PropertyHost], properties[provider.PropertyPort])
	}
	if properties[provider.PropertyPassword] != googletest.StoreToken || properties[provider.PropertyUsername] != "default" {
		t.Errorf("binding authenticates as %q with %q, want the default user with the newest active token the service generated",
			properties[provider.PropertyUsername], properties[provider.PropertyPassword])
	}
	if properties[provider.PropertyTLS] != "true" || properties[provider.PropertyCAPEM] != googletest.StoreAuthority {
		t.Errorf("binding tls %q trusting %q, want TLS under the instance's own certificate authority", properties[provider.PropertyTLS], properties[provider.PropertyCAPEM])
	}
}

func aPreviewKVStore(t *testing.T, spec provider.KVSpec) resources.ProvisionRequest {
	t.Helper()
	in := aKVStore(t, spec)
	in.Ref = provider.StackRef{Project: "shop", Tier: environment.TierPreview, Name: naming.InfraStack("prod")}
	return in
}

func TestAProductionStoreIsCreatedWithOneReplicaAndAPreviewStoreWithNone(t *testing.T) {
	for name, tiered := range map[string]struct {
		in       func(*testing.T, provider.KVSpec) resources.ProvisionRequest
		replicas int
	}{
		"production": {in: aKVStore, replicas: 1},
		"preview":    {in: aPreviewKVStore, replicas: 0},
	} {
		t.Run(name, func(t *testing.T) {
			p, server := servingMemorystore(t)

			if _, err := p.Provision(context.Background(), tiered.in(t, provider.KVSpec{MemoryBytes: 256 << 20}), nil); err != nil {
				t.Fatalf("ProvisionKV() = %v", err)
			}
			created := server.Creates()
			if len(created) != 1 || created[0].ReplicaCount == nil || *created[0].ReplicaCount != tiered.replicas {
				t.Errorf("a %s store was created with replicas %s, want %d", name, googletest.JSON(t, created), tiered.replicas)
			}
		})
	}
}

func TestAProductionStoreWithoutAReplicaIsGivenOneInPlace(t *testing.T) {
	p, server := servingMemorystore(t)
	in := aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20})
	if _, err := p.Provision(context.Background(), in, nil); err != nil {
		t.Fatalf("ProvisionKV() = %v", err)
	}
	server.HeldWithReplicas(storeInstance(t), 0)

	if _, err := p.Provision(context.Background(), in, nil); err != nil {
		t.Fatalf("ProvisionKV() of a production store without a replica = %v", err)
	}
	if masks := server.Masks(); len(masks) != 1 || masks[0] != "replica_count" {
		t.Errorf("Memorystore was updated with masks %v, want the replica count alone", masks)
	}
	if current := server.Instance(storeInstance(t)); current.ReplicaCount == nil || *current.ReplicaCount != 1 {
		t.Errorf("the instance holds replicas %s, want one", googletest.JSON(t, current.ReplicaCount))
	}
	if len(server.Creates()) != 1 {
		t.Errorf("Memorystore was asked to create %d instances, want the one store updated in place", len(server.Creates()))
	}
}

func TestAProductionStoreWhoseMemoryAndReplicasChangedIsUpdatedByTwoRequests(t *testing.T) {
	p, server := servingMemorystore(t)
	if _, err := p.Provision(context.Background(), aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20}), nil); err != nil {
		t.Fatalf("ProvisionKV() = %v", err)
	}
	server.HeldWithReplicas(storeInstance(t), 0)

	if _, err := p.Provision(context.Background(), aKVStore(t, provider.KVSpec{MemoryBytes: 2 << 30}), nil); err != nil {
		t.Fatalf("ProvisionKV() with a new memory = %v", err)
	}
	if masks := server.Masks(); !slices.Equal(masks, []string{"node_type,engine_configs", "replica_count"}) {
		t.Errorf("Memorystore was updated with masks %v, want the node and its configs first, then the replica count on its own", masks)
	}
	current := server.Instance(storeInstance(t))
	if current.NodeType != "CUSTOM_MINI" || current.ReplicaCount == nil || *current.ReplicaCount != 1 {
		t.Errorf("the instance is a %s with replicas %s, want a CUSTOM_MINI with one replica", current.NodeType, googletest.JSON(t, current.ReplicaCount))
	}
}

func TestAProductionStoreWithoutAReplicaPlansAnUpdateNamingTheReplica(t *testing.T) {
	p, server := servingMemorystore(t)
	if _, err := p.Provision(context.Background(), aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20}), nil); err != nil {
		t.Fatal(err)
	}
	server.HeldWithReplicas(storeInstance(t), 0)

	action, reason, err := planKV(t, p, server, provider.KVSpec{MemoryBytes: 256 << 20})
	if err != nil || action != provider.ActionUpdate || !strings.Contains(reason, "replica count changes from 0 to 1") {
		t.Errorf("PlanKV() = %s %q, %v, want %s naming the replica count change", action, reason, err, provider.ActionUpdate)
	}
}

func TestAStoreSaysWhenItsCreateOperationRanFromAndTo(t *testing.T) {
	p, _ := servingMemorystore(t)
	log := &said{Log: progress.Discard()}

	if _, err := p.Provision(context.Background(), aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20}), log); err != nil {
		t.Fatalf("ProvisionKV() = %v", err)
	}
	if !slices.ContainsFunc(log.lines, func(line string) bool {
		return strings.Contains(line, "created "+googletest.CreatedAt) && strings.Contains(line, "ended "+googletest.EndedAt)
	}) {
		t.Errorf("ProvisionKV said %q, want the create operation's createTime and endTime named", log.lines)
	}
}

func TestAStoreRunsOnTheSmallestNodeWhoseKeyspaceHoldsItsMemory(t *testing.T) {
	t.Parallel()

	for memory, want := range map[int64]string{
		32 << 20:  "CUSTOM_PICO",
		256 << 20: "CUSTOM_PICO",
		1 << 30:   "CUSTOM_PICO",
		2 << 30:   "CUSTOM_MINI",
		3 << 30:   "STANDARD_SMALL",
		8 << 30:   "HIGHMEM_MEDIUM",
		16 << 30:  "STANDARD_LARGE",
		32 << 30:  "HIGHMEM_XLARGE",
	} {
		store, err := readKVStore(provider.Resource{Name: "cache", KV: &provider.KVSpec{MemoryBytes: memory}})
		if err != nil {
			t.Errorf("readKVStore(%d bytes) = %v", memory, err)
			continue
		}
		if store.node.nodeType != want {
			t.Errorf("a store of %d bytes runs on %s, want %s", memory, store.node.nodeType, want)
		}
	}
}

func TestTheStoreInventoryMatchesTheInstanceProvisionSends(t *testing.T) {
	t.Parallel()

	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		for _, memory := range []int64{256 << 20, 8 << 30} {
			store, err := readKVStoreOn(provider.Resource{Name: "cache", KV: &provider.KVSpec{MemoryBytes: memory}}, tier)
			if err != nil {
				t.Fatal(err)
			}
			sent := store.instance("acme-prod", "network", nil)
			listed := store.inventoryProperties("us-central1", sent)
			if listed["node_type"] != sent.NodeType {
				t.Errorf("lists node %v, provision sends %s", listed["node_type"], sent.NodeType)
			}
			if listed["replica_count"] != *sent.ReplicaCount || listed["shard_count"] != sent.ShardCount {
				t.Errorf("lists %v replicas and %v shards, provision sends %d and %d", listed["replica_count"], listed["shard_count"], *sent.ReplicaCount, sent.ShardCount)
			}
			if listed["persistence_config"].(map[string]any)["mode"] != sent.PersistenceConfig.Mode {
				t.Errorf("lists persistence %v, provision sends %s", listed["persistence_config"], sent.PersistenceConfig.Mode)
			}
		}
	}
}

func TestAStoreLargerThan32GBIsRefusedThoughALargerNodeExists(t *testing.T) {
	t.Parallel()

	_, err := readKVStore(provider.Resource{Name: "cache", KV: &provider.KVSpec{MemoryBytes: 32<<30 + 1}})
	if err == nil || !strings.Contains(err.Error(), "32gb") || !strings.Contains(err.Error(), "cache") {
		t.Errorf("readKVStore() = %v, want a store above 32gb refused naming the limit", err)
	}
}

func TestAStoreRunsTheValkeyVersionItDeclaresAndEvictsByItsPolicy(t *testing.T) {
	t.Parallel()

	store, err := readKVStore(provider.Resource{Name: "cache", KV: &provider.KVSpec{Version: "8", Eviction: "allkeys-lru", MemoryBytes: 256 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	if store.version != "VALKEY_8_0" || store.engineConfigs()[maxMemoryPolicyConfig] != "allkeys-lru" {
		t.Errorf("version %s evicting by %s, want Valkey 8.0 evicting by allkeys-lru", store.version, store.engineConfigs()[maxMemoryPolicyConfig])
	}
	if _, err := readKVStore(provider.Resource{Name: "cache", KV: &provider.KVSpec{Version: "7"}}); err == nil {
		t.Error("readKVStore(version 7) = nil, want a version Memorystore is not asked to run refused")
	}
}

func TestAStoreProvisionedAgainIsReadBackWithoutAnotherInstance(t *testing.T) {
	p, server := servingMemorystore(t)
	in := aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20})
	if _, err := p.Provision(context.Background(), in, nil); err != nil {
		t.Fatalf("ProvisionKV() = %v", err)
	}

	again, err := p.Provision(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("a second ProvisionKV() = %v", err)
	}
	if len(server.Creates()) != 1 || len(server.Masks()) != 0 {
		t.Errorf("a second provision created %d and updated %v, want the instance read back unchanged", len(server.Creates()), server.Masks())
	}
	if again.Properties[provider.PropertyPassword] != googletest.StoreToken {
		t.Errorf("a second provision handed out %q, want the token read back again", again.Properties[provider.PropertyPassword])
	}
}

func TestAStoreWhoseEvictionOrMemoryChangedIsUpdatedInPlace(t *testing.T) {
	p, server := servingMemorystore(t)
	if _, err := p.Provision(context.Background(), aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20}), nil); err != nil {
		t.Fatalf("ProvisionKV() = %v", err)
	}

	if _, err := p.Provision(context.Background(), aKVStore(t, provider.KVSpec{Eviction: "allkeys-lfu", MemoryBytes: 2 << 30}), nil); err != nil {
		t.Fatalf("ProvisionKV() with a new eviction and memory = %v", err)
	}
	if masks := server.Masks(); len(masks) != 1 || masks[0] != "node_type,engine_configs" {
		t.Errorf("Memorystore was updated with masks %v, want the node type and engine configs alone", masks)
	}
	current := server.Instance(storeInstance(t))
	if current.NodeType != "CUSTOM_MINI" || current.EngineConfigs[maxMemoryPolicyConfig] != "allkeys-lfu" ||
		current.EngineConfigs[maxMemoryConfig] != strconv.Itoa(2<<30) {
		t.Errorf("the instance is a %s with configs %v, want the store's new shape", current.NodeType, current.EngineConfigs)
	}
	if len(server.Creates()) != 1 {
		t.Errorf("Memorystore was asked to create %d instances, want the one store updated in place", len(server.Creates()))
	}
}

func TestAStoreIsProvisionedThroughThrottling(t *testing.T) {
	p, server := servingMemorystore(t)
	server.Throttles = 2

	if _, err := p.Provision(context.Background(), aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20}), nil); err != nil {
		t.Fatalf("ProvisionKV() through two 429s = %v, want throttling retried", err)
	}
}

func TestAStoreCreateRetriedThroughAnOutageIsAskedForOnceUnderOneRequestID(t *testing.T) {
	p, server := servingMemorystore(t)
	server.ThrottledWrites = 1

	if _, err := p.Provision(context.Background(), aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20}), nil); err != nil {
		t.Fatalf("ProvisionKV() through a 503 = %v", err)
	}
	ids := server.RequestIDs()
	if len(ids) != 2 || ids[0] == "" || ids[0] != ids[1] {
		t.Errorf("the create was sent under request ids %q, want one id on both attempts so Memorystore runs it once", ids)
	}
}

func TestAStoreStillBeingCreatedIsAwaitedRatherThanCreatedAgain(t *testing.T) {
	p, server := servingMemorystore(t)
	in := aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20})
	if _, err := p.Provision(context.Background(), in, nil); err != nil {
		t.Fatalf("ProvisionKV() = %v", err)
	}
	server.StillCreating(storeInstance(t), 2)

	binding, err := p.Provision(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("ProvisionKV() of a store still being created = %v, want it awaited", err)
	}
	if binding.Properties[provider.PropertyHost] != googletest.StoreAddress || len(server.Creates()) != 1 || len(server.Masks()) != 0 {
		t.Errorf("a store still being created bound %q after %d creates and updates %v, want it awaited and bound unchanged",
			binding.Properties[provider.PropertyHost], len(server.Creates()), server.Masks())
	}
}

func TestAStoreWhoseCreateFailsIsRefusedWithWhatMemorystoreSaid(t *testing.T) {
	p, server := servingMemorystore(t)
	server.Failed = "the service connection policy is missing"

	_, err := p.Provision(context.Background(), aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20}), nil)
	if err == nil || !strings.Contains(err.Error(), "service connection policy is missing") {
		t.Errorf("ProvisionKV() = %v, want the operation's error named", err)
	}
}

func TestRemovingAStoreDeletesItsInstanceAndAMissingOneIsAlreadyGone(t *testing.T) {
	p, server := servingMemorystore(t)
	in := aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20})
	binding, err := p.Provision(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("ProvisionKV() = %v", err)
	}

	if err := p.Remove(context.Background(), in.Ref, binding, nil); err != nil {
		t.Fatalf("RemoveKV() = %v", err)
	}
	if deleted := server.Deletes(); len(deleted) != 1 || deleted[0] != storeInstance(t) {
		t.Errorf("Memorystore deleted %v, want the store's instance", deleted)
	}
	if err := p.Remove(context.Background(), in.Ref, binding, nil); err != nil {
		t.Errorf("RemoveKV() of a store already gone = %v, want nil", err)
	}
}

func TestAStoreMemorystoreHoldsInAStateThatNeverSettlesIsRefusedWithoutWaiting(t *testing.T) {
	for _, state := range []string{"DELETING", "FAILED", "SUSPENDED", "STATE_UNSPECIFIED"} {
		t.Run(state, func(t *testing.T) {
			p, server := servingMemorystore(t)
			in := aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20})
			if _, err := p.Provision(context.Background(), in, nil); err != nil {
				t.Fatalf("ProvisionKV() = %v", err)
			}
			server.HeldIn(storeInstance(t), state)
			before := server.InstanceReads()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := p.Provision(ctx, in, nil)
			var refused refusal.Refusal
			if !errors.As(err, &refused) || !strings.Contains(refused.Message, state) || !strings.Contains(refused.Message, "cache") {
				t.Errorf("ProvisionKV() of a store held %s = %v, want it refused naming the store and its state", state, err)
			}
			if reads := server.InstanceReads() - before; reads != 1 {
				t.Errorf("a store held %s was read %d times, want it refused on the read that sees the state rather than awaited", state, reads)
			}
		})
	}
}

func planKV(t *testing.T, p *KV, server *googletest.Memorystore, spec provider.KVSpec) (provider.ChangeAction, string, error) {
	t.Helper()
	before := len(server.RequestIDs())
	action, reason, err := p.Plan(context.Background(), aKVStore(t, spec), nil)
	if wrote := len(server.RequestIDs()) - before; wrote != 0 {
		t.Errorf("PlanKV() sent %d writes to Memorystore, want a plan that only reads", wrote)
	}
	return action, reason, err
}

func TestAMissingStorePlansACreate(t *testing.T) {
	p, server := servingMemorystore(t)

	action, _, err := planKV(t, p, server, provider.KVSpec{MemoryBytes: 256 << 20})
	if err != nil || action != provider.ActionCreate {
		t.Errorf("PlanKV() = %s, %v, want %s", action, err, provider.ActionCreate)
	}
}

func TestAStoreAsDeclaredPlansKeep(t *testing.T) {
	p, server := servingMemorystore(t)
	if _, err := p.Provision(context.Background(), aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20}), nil); err != nil {
		t.Fatal(err)
	}

	action, reason, err := planKV(t, p, server, provider.KVSpec{MemoryBytes: 256 << 20})
	if err != nil || action != provider.ActionKeep || reason != provider.ReasonCurrent {
		t.Errorf("PlanKV() = %s %q, %v, want %s %q", action, reason, err, provider.ActionKeep, provider.ReasonCurrent)
	}
}

func TestAStoreWhoseEvictionOrMemoryChangedPlansAnUpdateNamingWhatChanges(t *testing.T) {
	p, server := servingMemorystore(t)
	if _, err := p.Provision(context.Background(), aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20}), nil); err != nil {
		t.Fatal(err)
	}

	action, reason, err := planKV(t, p, server, provider.KVSpec{Eviction: "allkeys-lfu", MemoryBytes: 2 << 30})
	if err != nil || action != provider.ActionUpdate {
		t.Fatalf("PlanKV() = %s, %v, want %s", action, err, provider.ActionUpdate)
	}
	for _, want := range []string{"node type changes from CUSTOM_PICO to CUSTOM_MINI", "maxmemory-policy changes from noeviction to allkeys-lfu"} {
		if !strings.Contains(reason, want) {
			t.Errorf("PlanKV() reason = %q, want it to say %q", reason, want)
		}
	}
}

func TestAStoreStillSettlingPlansAnUpdate(t *testing.T) {
	p, server := servingMemorystore(t)
	if _, err := p.Provision(context.Background(), aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20}), nil); err != nil {
		t.Fatal(err)
	}
	server.HeldIn(storeInstance(t), "UPDATING")

	if action, _, err := planKV(t, p, server, provider.KVSpec{MemoryBytes: 256 << 20}); err != nil || action != provider.ActionUpdate {
		t.Errorf("PlanKV() of a store Memorystore is updating = %s, %v, want %s: the deploy cannot know what it applies once it settles", action, err, provider.ActionUpdate)
	}
}

func TestAStoreHeldInAStateThatNeverSettlesIsRefusedAtPlan(t *testing.T) {
	p, server := servingMemorystore(t)
	if _, err := p.Provision(context.Background(), aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20}), nil); err != nil {
		t.Fatal(err)
	}
	server.HeldIn(storeInstance(t), "FAILED")

	_, _, err := planKV(t, p, server, provider.KVSpec{MemoryBytes: 256 << 20})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "FAILED") {
		t.Errorf("PlanKV() of a store held FAILED = %v, want the refusal the provision would give", err)
	}
}

func TestAStoreCreateAnsweredWithAnUnnamedUnfinishedOperationIsAnError(t *testing.T) {
	p, server := servingMemorystore(t)
	server.Unnamed = true

	_, err := p.Provision(context.Background(), aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20}), nil)
	if err == nil || !strings.Contains(err.Error(), "no name") {
		t.Errorf("ProvisionKV() = %v, want an operation nothing can poll named as an error rather than taken as done", err)
	}
}
