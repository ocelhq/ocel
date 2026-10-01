package gcp

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/kvstore"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	clusterModeOff        = "CLUSTER_DISABLED"
	appendOnly            = "AOF"
	fsyncEverySecond      = "EVERY_SEC"
	tokenAuth             = "TOKEN_AUTH"
	serverAuthentication  = "SERVER_AUTHENTICATION"
	maxMemoryConfig       = "maxmemory"
	maxMemoryPolicyConfig = "maxmemory-policy"
	defaultEviction       = "noeviction"
	defaultValkeyPort     = 6379
)

type memorystoreNode struct {
	nodeType      string
	keyspaceBytes int64
	capacityGB    float64
}

func (n memorystoreNode) terraformNodeType() string {
	return strings.ToLower(strings.ReplaceAll(n.nodeType, "_", "-"))
}

var memorystoreNodes = []memorystoreNode{
	{nodeType: "CUSTOM_PICO", keyspaceBytes: 1_080_000_000, capacityGB: 1.25},
	{nodeType: "CUSTOM_MICRO", keyspaceBytes: 2_000_000_000, capacityGB: 2.5},
	{nodeType: "CUSTOM_MINI", keyspaceBytes: 3_000_000_000, capacityGB: 3.75},
	{nodeType: "STANDARD_SMALL", keyspaceBytes: 5_200_000_000, capacityGB: 6.5},
	{nodeType: "HIGHMEM_MEDIUM", keyspaceBytes: 10_400_000_000, capacityGB: 13},
	{nodeType: "STANDARD_LARGE", keyspaceBytes: 20_800_000_000, capacityGB: 26},
	{nodeType: "HIGHMEM_XLARGE", keyspaceBytes: 46_400_000_000, capacityGB: 58},
}

var engineVersions = map[string]string{"8": "VALKEY_8_0", "9": "VALKEY_9_1"}

type kvStore struct {
	version     string
	node        memorystoreNode
	memoryBytes int64
	eviction    string
}

func readKVStore(resource provider.Resource) (kvStore, error) {
	spec := provider.KVSpec{}
	if resource.KV != nil {
		spec = *resource.KV
	}
	if spec.MemoryBytes == 0 {
		spec.MemoryBytes, _ = kvstore.ParseMemory(kvstore.DefaultMemory)
	}
	version := cmp.Or(spec.Version, kvstore.DefaultVersion)
	engine, known := engineVersions[version]
	if !known {
		return kvStore{}, refusal.Refuse(refusal.CodeInvalid,
			"kv %s asks for version %q, and Memorystore runs a store at version 8 or 9", resource.Name, version)
	}
	if spec.MemoryBytes > kvstore.MaxMemoryBytes {
		return kvStore{}, refusal.Refuse(refusal.CodeInvalid,
			"kv %s declares %d bytes of memory, and a store holds at most %dgb, on one Memorystore node: declare %dgb or less",
			resource.Name, spec.MemoryBytes, kvstore.MaxMemoryBytes>>30, kvstore.MaxMemoryBytes>>30)
	}
	at := slices.IndexFunc(memorystoreNodes, func(node memorystoreNode) bool { return node.keyspaceBytes >= spec.MemoryBytes })
	return kvStore{version: engine, node: memorystoreNodes[at], memoryBytes: spec.MemoryBytes, eviction: cmp.Or(spec.Eviction, defaultEviction)}, nil
}

func (s kvStore) engineConfigs() map[string]string {
	return map[string]string{
		maxMemoryConfig:       strconv.FormatInt(s.memoryBytes, 10),
		maxMemoryPolicyConfig: s.eviction,
	}
}

func (s kvStore) instance(project, network string, labels map[string]string) *memorystoreInstance {
	none, off := 0, false
	return &memorystoreInstance{
		Labels:                    labels,
		Mode:                      clusterModeOff,
		ShardCount:                1,
		ReplicaCount:              &none,
		NodeType:                  s.node.nodeType,
		EngineVersion:             s.version,
		EngineConfigs:             s.engineConfigs(),
		AuthorizationMode:         tokenAuth,
		TransitEncryptionMode:     serverAuthentication,
		PersistenceConfig:         &persistenceConfig{Mode: appendOnly, AOFConfig: &aofConfig{AppendFsync: fsyncEverySecond}},
		DeletionProtectionEnabled: &off,
		Endpoints: []instanceEndpoint{{Connections: []endpointConnection{
			{PSCAutoConnection: &pscAutoConnection{ProjectID: project, Network: network}},
		}}},
	}
}

func (s kvStore) shapeProperties(region string) map[string]any {
	return map[string]any{
		"location":           region,
		"mode":               clusterModeOff,
		"node_type":          s.node.terraformNodeType(),
		"node_capacity_gb":   s.node.capacityGB,
		"shard_count":        1,
		"replica_count":      0,
		"persistence_config": map[string]any{"mode": appendOnly, "aof_config": map[string]any{"append_fsync": fsyncEverySecond}},
	}
}

func (s kvStore) changes(current *memorystoreInstance) []string {
	var mask []string
	if current.NodeType != s.node.nodeType {
		mask = append(mask, "node_type")
	}
	if current.EngineVersion != s.version {
		mask = append(mask, "engine_version")
	}
	want := s.engineConfigs()
	if current.EngineConfigs[maxMemoryConfig] != want[maxMemoryConfig] || current.EngineConfigs[maxMemoryPolicyConfig] != want[maxMemoryPolicyConfig] {
		mask = append(mask, "engine_configs")
	}
	return mask
}

func labelsFor(names Names, ref provider.StackRef, store string) map[string]string {
	return map[string]string{
		"ocel-namespace":   naming.Sanitize(string(names.namespace)),
		"ocel-tier":        string(ref.Tier),
		"ocel-project":     naming.Sanitize(ref.Project),
		"ocel-environment": naming.Sanitize(ref.Name.Env),
		"ocel-kv":          naming.Sanitize(store),
	}
}

type kvInstance struct {
	id        string
	path      string
	storeName string
	service   memorystore
}

func (p *Provider) openKVInstance(ctx context.Context, ref provider.StackRef, store string) (*clients, kvInstance, error) {
	clients, err := p.openClients(ctx)
	if err != nil {
		return nil, kvInstance{}, err
	}
	id, err := clients.KVInstance(ref.Project, ref.Name.Env, store)
	if err != nil {
		return nil, kvInstance{}, err
	}
	return clients, kvInstance{id: id, path: clients.location() + "/instances/" + id, storeName: store, service: p.memorystore()}, nil
}

func (p *Provider) ProvisionKV(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	store, err := readKVStore(in.Resource)
	if err != nil {
		return provider.Binding{}, err
	}
	clients, instance, err := p.openKVInstance(ctx, in.Ref, in.Resource.Name)
	if err != nil {
		return provider.Binding{}, err
	}
	current, err := instance.service.readInstance(ctx, instance.path)
	switch {
	case absent(err):
		desired := store.instance(clients.project, clients.NetworkPath(in.Ref.Tier), labelsFor(clients.Names, in.Ref, in.Resource.Name))
		current, err = instance.create(ctx, clients, desired, progress)
	case err != nil:
		return provider.Binding{}, fmt.Errorf("read the Memorystore instance kv %s runs on: %w", in.Resource.Name, err)
	case isSettling(current.State):
		if current, err = instance.service.readSettledInstance(ctx, instance.path, in.Resource.Name); err == nil {
			if err = instance.refuseInactive(current, clients.region); err == nil {
				current, err = instance.update(ctx, store, current, progress)
			}
		}
	case current.State != instanceActive:
		err = instance.refuseInactive(current, clients.region)
	default:
		current, err = instance.update(ctx, store, current, progress)
	}
	if err != nil {
		return provider.Binding{}, err
	}
	return instance.readBinding(ctx, current, in.Resource)
}

func (i kvInstance) create(ctx context.Context, clients *clients, desired *memorystoreInstance, progress progress.Log) (*memorystoreInstance, error) {
	ensureProgress(progress).Say("Creating kv " + i.storeName + " as Memorystore instance " + i.id + " in " + clients.region +
		": a new instance takes several minutes")
	started, err := i.service.createInstance(ctx, clients.location(), i.id, desired)
	if err != nil {
		return nil, fmt.Errorf("create the Memorystore instance kv %s runs on: %w", i.storeName, err)
	}
	finished, err := i.service.awaited(ctx, "creating kv "+i.storeName, started)
	if err != nil {
		return nil, err
	}
	if timed := finished.Metadata; timed != nil {
		ensureProgress(progress).Say("Created kv " + i.storeName + ": its create operation " + finished.Name +
			" created " + timed.CreateTime + " and ended " + timed.EndTime)
	}
	return i.service.readInstance(ctx, i.path)
}

func (i kvInstance) update(ctx context.Context, store kvStore, current *memorystoreInstance, progress progress.Log) (*memorystoreInstance, error) {
	mask := store.changes(current)
	if len(mask) == 0 {
		return current, nil
	}
	ensureProgress(progress).Say("Updating kv " + i.storeName + "'s " + strings.Join(mask, ", ") + " on Memorystore instance " + i.id)
	started, err := i.service.updateInstance(ctx, i.path, mask, &memorystoreInstance{
		NodeType:      store.node.nodeType,
		EngineVersion: store.version,
		EngineConfigs: store.engineConfigs(),
	})
	if err != nil {
		return nil, fmt.Errorf("update the Memorystore instance kv %s runs on: %w", i.storeName, err)
	}
	if _, err := i.service.awaited(ctx, "updating kv "+i.storeName, started); err != nil {
		return nil, err
	}
	return i.service.readInstance(ctx, i.path)
}

func (i kvInstance) refuseInactive(current *memorystoreInstance, region string) error {
	if current.State == instanceActive {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"kv %s runs on the Memorystore instance %s, which is %s, and only an instance Memorystore is creating or updating becomes %s by itself: "+
			"a store is bound and updated only once it is %s.\n"+
			"If it is being deleted, deploy again once it is gone. Otherwise delete it with `gcloud memorystore instances delete %s --location %s`, "+
			"which deletes the data it holds, and deploy again to create it afresh",
		i.storeName, i.id, current.State, instanceActive, instanceActive, i.id, region)
}

func (i kvInstance) readBinding(ctx context.Context, current *memorystoreInstance, resource provider.Resource) (provider.Binding, error) {
	host, port, err := primaryAddressOf(current, resource.Name)
	if err != nil {
		return provider.Binding{}, err
	}
	tokens, err := i.service.listDefaultTokens(ctx, i.path)
	if err != nil {
		return provider.Binding{}, fmt.Errorf("read the token kv %s authenticates with: %w", resource.Name, err)
	}
	token, err := newestActiveTokenOf(tokens, resource.Name)
	if err != nil {
		return provider.Binding{}, err
	}
	authority, err := i.service.readCertificateAuthority(ctx, i.path)
	if err != nil {
		return provider.Binding{}, fmt.Errorf("read the certificate authority kv %s serves TLS under: %w", resource.Name, err)
	}
	pem, err := authorityPEMOf(authority, resource.Name)
	if err != nil {
		return provider.Binding{}, err
	}
	return provider.Binding{
		Type:     provider.BindingKV,
		Name:     resource.Name,
		Resource: resource.Declared,
		Properties: map[string]string{
			provider.PropertyHost:     host,
			provider.PropertyPort:     strconv.Itoa(port),
			provider.PropertyUsername: defaultTokenUser,
			provider.PropertyPassword: token,
			provider.PropertyTLS:      "true",
			provider.PropertyCAPEM:    pem,
		},
	}, nil
}

func primaryAddressOf(instance *memorystoreInstance, store string) (string, int, error) {
	for _, endpoint := range instance.Endpoints {
		for _, connection := range endpoint.Connections {
			auto := connection.PSCAutoConnection
			if auto == nil || auto.IPAddress == "" || auto.ConnectionType != primaryEndpoint {
				continue
			}
			return auto.IPAddress, cmp.Or(auto.Port, defaultValkeyPort), nil
		}
	}
	return "", 0, refusal.Refuse(refusal.CodeNotReady,
		"the Memorystore instance kv %s runs on has no primary endpoint yet, so nothing names the address an app reaches it at", store)
}

func newestActiveTokenOf(tokens []authToken, store string) (string, error) {
	var newest *authToken
	for i := range tokens {
		if tokens[i].State != tokenActive || tokens[i].Token == "" {
			continue
		}
		if newest == nil || tokens[i].CreateTime > newest.CreateTime {
			newest = &tokens[i]
		}
	}
	if newest == nil {
		return "", refusal.Refuse(refusal.CodeNotReady,
			"the Memorystore instance kv %s runs on has no active token for its default user, and an app authenticates with one", store)
	}
	return newest.Token, nil
}

func authorityPEMOf(authority *certificateAuthority, store string) (string, error) {
	var pem strings.Builder
	if authority.ManagedServerCA != nil {
		for _, chain := range authority.ManagedServerCA.CACerts {
			for _, certificate := range chain.Certificates {
				pem.WriteString(strings.TrimRight(certificate, "\n") + "\n")
			}
		}
	}
	if pem.Len() == 0 {
		return "", refusal.Refuse(refusal.CodeNotReady,
			"the Memorystore instance kv %s runs on names no certificate authority, and an app verifies the store's TLS against it", store)
	}
	return pem.String(), nil
}

func (p *Provider) removeKV(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	_, instance, err := p.openKVInstance(ctx, ref, binding.Name)
	if err != nil {
		return err
	}
	ensureProgress(progress).Say("Removing kv " + binding.Name + ", its Memorystore instance " + instance.id + " and its data")
	started, err := instance.service.deleteInstance(ctx, instance.path)
	if absent(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete the Memorystore instance kv %s runs on: %w", binding.Name, err)
	}
	_, err = instance.service.awaited(ctx, "deleting kv "+binding.Name, started)
	return err
}
