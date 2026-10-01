package gcp

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
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func aKVStore(t *testing.T, spec provider.KVSpec) resources.ProvisionRequest {
	t.Helper()
	return resources.ProvisionRequest{
		Ref:      provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack("prod")},
		Resource: provider.Resource{Name: "cache", Type: provider.BindingKV, KV: &spec},
	}
}

func storeInstance(t *testing.T, p *Provider) string {
	t.Helper()
	instance, err := names(t, p).KVInstance("shop", "prod", "cache")
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

	binding, err := p.ProvisionKV(context.Background(), aKVStore(t, provider.KVSpec{Version: "9", MemoryBytes: 256 << 20}), nil)
	if err != nil {
		t.Fatalf("ProvisionKV() = %v", err)
	}

	created := server.creates()
	if len(created) != 1 {
		t.Fatalf("Memorystore was asked to create %d instances, want one per store", len(created))
	}
	sent := created[0]
	if sent.Mode != "CLUSTER_DISABLED" || sent.ShardCount != 1 || sent.ReplicaCount == nil || *sent.ReplicaCount != 0 {
		t.Errorf("created mode %q with %d shards and replicas %v, want cluster mode off on one node", sent.Mode, sent.ShardCount, sent.ReplicaCount)
	}
	if sent.PersistenceConfig == nil || sent.PersistenceConfig.Mode != "AOF" || sent.PersistenceConfig.AOFConfig == nil ||
		sent.PersistenceConfig.AOFConfig.AppendFsync != "EVERY_SEC" {
		t.Errorf("created persistence %s, want the append-only file fsynced every second", asJSON(t, sent.PersistenceConfig))
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
	want := pscAutoConnection{ProjectID: "acme-prod", Network: "projects/acme-prod/global/networks/ocel-production"}
	if len(sent.Endpoints) != 1 || len(sent.Endpoints[0].Connections) != 1 || sent.Endpoints[0].Connections[0].PSCAutoConnection == nil ||
		*sent.Endpoints[0].Connections[0].PSCAutoConnection != want {
		t.Errorf("created endpoints %s, want one automatic connection into the tier's network", asJSON(t, sent.Endpoints))
	}

	if err := provider.VerifyProperties(binding); err != nil {
		t.Fatalf("VerifyProperties() = %v", err)
	}
	properties := binding.Properties
	if binding.Name != "cache" || binding.Type != provider.BindingKV {
		t.Errorf("binding = %s %s, want kv cache", binding.Type, binding.Name)
	}
	if properties[provider.PropertyHost] != storeAddress || properties[provider.PropertyPort] != "6379" {
		t.Errorf("binding reaches %s:%s, want the instance's primary endpoint", properties[provider.PropertyHost], properties[provider.PropertyPort])
	}
	if properties[provider.PropertyPassword] != storeToken || properties[provider.PropertyUsername] != "default" {
		t.Errorf("binding authenticates as %q with %q, want the default user with the newest active token the service generated",
			properties[provider.PropertyUsername], properties[provider.PropertyPassword])
	}
	if properties[provider.PropertyTLS] != "true" || properties[provider.PropertyCAPEM] != storeAuthority {
		t.Errorf("binding tls %q trusting %q, want TLS under the instance's own certificate authority", properties[provider.PropertyTLS], properties[provider.PropertyCAPEM])
	}
}

func TestAStoreSaysWhenItsCreateOperationRanFromAndTo(t *testing.T) {
	p, _ := servingMemorystore(t)
	log := &said{Log: progress.Discard()}

	if _, err := p.ProvisionKV(context.Background(), aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20}), log); err != nil {
		t.Fatalf("ProvisionKV() = %v", err)
	}
	if !slices.ContainsFunc(log.lines, func(line string) bool {
		return strings.Contains(line, "created "+createdAt) && strings.Contains(line, "ended "+endedAt)
	}) {
		t.Errorf("ProvisionKV said %q, want the create operation's createTime and endTime named", log.lines)
	}
}

func TestAStoreRunsOnTheSmallestNodeWhoseKeyspaceHoldsItsMemory(t *testing.T) {
	t.Parallel()

	for memory, want := range map[int64]string{
		32 << 20:       "CUSTOM_PICO",
		256 << 20:      "CUSTOM_PICO",
		1 << 30:        "CUSTOM_PICO",
		2 << 30:        "CUSTOM_MINI",
		3 << 30:        "STANDARD_SMALL",
		8 << 30:        "HIGHMEM_MEDIUM",
		16 << 30:       "STANDARD_LARGE",
		32 << 30:       "HIGHMEM_XLARGE",
		46_400_000_000: "HIGHMEM_XLARGE",
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

func TestTheStoreShapeMatchesTheInstanceProvisionSends(t *testing.T) {
	t.Parallel()

	for _, memory := range []int64{256 << 20, 8 << 30} {
		store, err := readKVStore(provider.Resource{Name: "cache", KV: &provider.KVSpec{MemoryBytes: memory}})
		if err != nil {
			t.Fatal(err)
		}
		sent := store.instance("acme-prod", "network", nil)
		shaped := store.shapeProperties("us-central1")
		if strings.ReplaceAll(strings.ToUpper(shaped["node_type"].(string)), "-", "_") != sent.NodeType {
			t.Errorf("shaped node %v, provision sends %s", shaped["node_type"], sent.NodeType)
		}
		if shaped["replica_count"] != *sent.ReplicaCount || shaped["shard_count"] != sent.ShardCount {
			t.Errorf("shaped %v replicas and %v shards, provision sends %d and %d", shaped["replica_count"], shaped["shard_count"], *sent.ReplicaCount, sent.ShardCount)
		}
		if shaped["persistence_config"].(map[string]any)["mode"] != sent.PersistenceConfig.Mode {
			t.Errorf("shaped persistence %v, provision sends %s", shaped["persistence_config"], sent.PersistenceConfig.Mode)
		}
	}
}

func TestAStoreLargerThanEveryNodeIsRefusedNamingTheLargest(t *testing.T) {
	t.Parallel()

	_, err := readKVStore(provider.Resource{Name: "cache", KV: &provider.KVSpec{MemoryBytes: 46_400_000_001}})
	if err == nil || !strings.Contains(err.Error(), "highmem-xlarge") || !strings.Contains(err.Error(), "cache") {
		t.Errorf("readKVStore() = %v, want the store refused naming the largest node", err)
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
	if _, err := p.ProvisionKV(context.Background(), in, nil); err != nil {
		t.Fatalf("ProvisionKV() = %v", err)
	}

	again, err := p.ProvisionKV(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("a second ProvisionKV() = %v", err)
	}
	if len(server.creates()) != 1 || len(server.masks()) != 0 {
		t.Errorf("a second provision created %d and updated %v, want the instance read back unchanged", len(server.creates()), server.masks())
	}
	if again.Properties[provider.PropertyPassword] != storeToken {
		t.Errorf("a second provision handed out %q, want the token read back again", again.Properties[provider.PropertyPassword])
	}
}

func TestAStoreWhoseEvictionOrMemoryChangedIsUpdatedInPlace(t *testing.T) {
	p, server := servingMemorystore(t)
	if _, err := p.ProvisionKV(context.Background(), aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20}), nil); err != nil {
		t.Fatalf("ProvisionKV() = %v", err)
	}

	if _, err := p.ProvisionKV(context.Background(), aKVStore(t, provider.KVSpec{Eviction: "allkeys-lfu", MemoryBytes: 2 << 30}), nil); err != nil {
		t.Fatalf("ProvisionKV() with a new eviction and memory = %v", err)
	}
	if masks := server.masks(); len(masks) != 1 || masks[0] != "node_type,engine_configs" {
		t.Errorf("Memorystore was updated with masks %v, want the node type and engine configs alone", masks)
	}
	current := server.instance(storeInstance(t, p))
	if current.NodeType != "CUSTOM_MINI" || current.EngineConfigs[maxMemoryPolicyConfig] != "allkeys-lfu" ||
		current.EngineConfigs[maxMemoryConfig] != strconv.Itoa(2<<30) {
		t.Errorf("the instance is a %s with configs %v, want the store's new shape", current.NodeType, current.EngineConfigs)
	}
	if len(server.creates()) != 1 {
		t.Errorf("Memorystore was asked to create %d instances, want the one store updated in place", len(server.creates()))
	}
}

func TestAStoreIsProvisionedThroughThrottling(t *testing.T) {
	p, server := servingMemorystore(t)
	server.throttles = 2

	if _, err := p.ProvisionKV(context.Background(), aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20}), nil); err != nil {
		t.Fatalf("ProvisionKV() through two 429s = %v, want throttling retried", err)
	}
}

func TestAStoreCreateRetriedThroughAnOutageIsAskedForOnceUnderOneRequestID(t *testing.T) {
	p, server := servingMemorystore(t)
	server.throttledWrites = 1

	if _, err := p.ProvisionKV(context.Background(), aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20}), nil); err != nil {
		t.Fatalf("ProvisionKV() through a 503 = %v", err)
	}
	ids := server.requestIDs()
	if len(ids) != 2 || ids[0] == "" || ids[0] != ids[1] {
		t.Errorf("the create was sent under request ids %q, want one id on both attempts so Memorystore runs it once", ids)
	}
}

func TestAStoreStillBeingCreatedIsAwaitedRatherThanCreatedAgain(t *testing.T) {
	p, server := servingMemorystore(t)
	in := aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20})
	if _, err := p.ProvisionKV(context.Background(), in, nil); err != nil {
		t.Fatalf("ProvisionKV() = %v", err)
	}
	server.stillCreating(storeInstance(t, p), 2)

	binding, err := p.ProvisionKV(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("ProvisionKV() of a store still being created = %v, want it awaited", err)
	}
	if binding.Properties[provider.PropertyHost] != storeAddress || len(server.creates()) != 1 || len(server.masks()) != 0 {
		t.Errorf("a store still being created bound %q after %d creates and updates %v, want it awaited and bound unchanged",
			binding.Properties[provider.PropertyHost], len(server.creates()), server.masks())
	}
}

func TestAStoreWhoseCreateFailsIsRefusedWithWhatMemorystoreSaid(t *testing.T) {
	p, server := servingMemorystore(t)
	server.failed = "the service connection policy is missing"

	_, err := p.ProvisionKV(context.Background(), aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20}), nil)
	if err == nil || !strings.Contains(err.Error(), "service connection policy is missing") {
		t.Errorf("ProvisionKV() = %v, want the operation's error named", err)
	}
}

func TestRemovingAStoreDeletesItsInstanceAndAMissingOneIsAlreadyGone(t *testing.T) {
	p, server := servingMemorystore(t)
	in := aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20})
	binding, err := p.ProvisionKV(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("ProvisionKV() = %v", err)
	}

	if err := p.RemoveResource(context.Background(), in.Ref, binding, nil); err != nil {
		t.Fatalf("RemoveResource() = %v", err)
	}
	if deleted := server.deletes(); len(deleted) != 1 || deleted[0] != storeInstance(t, p) {
		t.Errorf("Memorystore deleted %v, want the store's instance", deleted)
	}
	if err := p.RemoveResource(context.Background(), in.Ref, binding, nil); err != nil {
		t.Errorf("RemoveResource() of a store already gone = %v, want nil", err)
	}
}

func TestAStoreMemorystoreHoldsInAStateThatNeverSettlesIsRefusedWithoutWaiting(t *testing.T) {
	for _, state := range []string{"DELETING", "FAILED", "SUSPENDED", "STATE_UNSPECIFIED"} {
		t.Run(state, func(t *testing.T) {
			p, server := servingMemorystore(t)
			in := aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20})
			if _, err := p.ProvisionKV(context.Background(), in, nil); err != nil {
				t.Fatalf("ProvisionKV() = %v", err)
			}
			server.heldIn(storeInstance(t, p), state)
			before := server.instanceReads()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := p.ProvisionKV(ctx, in, nil)
			var refused refusal.Refusal
			if !errors.As(err, &refused) || !strings.Contains(refused.Message, state) || !strings.Contains(refused.Message, "cache") {
				t.Errorf("ProvisionKV() of a store held %s = %v, want it refused naming the store and its state", state, err)
			}
			if reads := server.instanceReads() - before; reads != 1 {
				t.Errorf("a store held %s was read %d times, want it refused on the read that sees the state rather than awaited", state, reads)
			}
		})
	}
}

func TestAStoreCreateAnsweredWithAnUnnamedUnfinishedOperationIsAnError(t *testing.T) {
	p, server := servingMemorystore(t)
	server.unnamed = true

	_, err := p.ProvisionKV(context.Background(), aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20}), nil)
	if err == nil || !strings.Contains(err.Error(), "no name") {
		t.Errorf("ProvisionKV() = %v, want an operation nothing can poll named as an error rather than taken as done", err)
	}
}
