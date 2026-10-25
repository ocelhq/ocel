package kv

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/valkey"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
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
	productionReplicas    = 1
	Port                  = 6379
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
	replicas    int
}

func readKVStore(resource provider.Resource) (kvStore, error) {
	spec := provider.KVSpec{}
	if resource.KV != nil {
		spec = *resource.KV
	}
	if spec.MemoryBytes == 0 {
		spec.MemoryBytes, _ = valkey.ParseMemory(valkey.DefaultMemory)
	}
	version := cmp.Or(spec.Version, valkey.DefaultVersion)
	engine, known := engineVersions[version]
	if !known {
		return kvStore{}, refusal.Refuse(refusal.CodeInvalid,
			"kv %s asks for version %q, and Memorystore runs a store at version 8 or 9", resource.Name, version)
	}
	if spec.MemoryBytes > valkey.MaxMemoryBytes {
		return kvStore{}, refusal.Refuse(refusal.CodeInvalid,
			"kv %s declares %d bytes of memory, and a store holds at most %dgb, on one Memorystore node: declare %dgb or less",
			resource.Name, spec.MemoryBytes, valkey.MaxMemoryBytes>>30, valkey.MaxMemoryBytes>>30)
	}
	at := slices.IndexFunc(memorystoreNodes, func(node memorystoreNode) bool { return node.keyspaceBytes >= spec.MemoryBytes })
	return kvStore{version: engine, node: memorystoreNodes[at], memoryBytes: spec.MemoryBytes, eviction: cmp.Or(spec.Eviction, defaultEviction)}, nil
}

func readKVStoreOn(resource provider.Resource, tier environment.Tier) (kvStore, error) {
	store, err := readKVStore(resource)
	if err != nil {
		return kvStore{}, err
	}
	if tier == environment.TierProduction {
		store.replicas = productionReplicas
	}
	return store, nil
}

func (s kvStore) engineConfigs() map[string]string {
	return map[string]string{
		maxMemoryConfig:       strconv.FormatInt(s.memoryBytes, 10),
		maxMemoryPolicyConfig: s.eviction,
	}
}

func (s kvStore) instance(project, network string, labels map[string]string) *gcloud.MemorystoreInstance {
	replicas, off := s.replicas, false
	return &gcloud.MemorystoreInstance{
		Labels:                    labels,
		Mode:                      clusterModeOff,
		ShardCount:                1,
		ReplicaCount:              &replicas,
		NodeType:                  s.node.nodeType,
		EngineVersion:             s.version,
		EngineConfigs:             s.engineConfigs(),
		AuthorizationMode:         tokenAuth,
		TransitEncryptionMode:     serverAuthentication,
		PersistenceConfig:         &gcloud.PersistenceConfig{Mode: appendOnly, AOFConfig: &gcloud.AOFConfig{AppendFsync: fsyncEverySecond}},
		DeletionProtectionEnabled: &off,
		Endpoints: []gcloud.InstanceEndpoint{{Connections: []gcloud.EndpointConnection{
			{PSCAutoConnection: &gcloud.PSCAutoConnection{ProjectID: project, Network: network}},
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
		"replica_count":      s.replicas,
		"persistence_config": map[string]any{"mode": appendOnly, "aof_config": map[string]any{"append_fsync": fsyncEverySecond}},
	}
}

func (s kvStore) changes(current *gcloud.MemorystoreInstance) ([]string, []string) {
	var mask, differences []string
	if current.NodeType != s.node.nodeType {
		mask = append(mask, "node_type")
		differences = append(differences, fmt.Sprintf("node type changes from %s to %s", current.NodeType, s.node.nodeType))
	}
	if current.EngineVersion != s.version {
		mask = append(mask, "engine_version")
		differences = append(differences, fmt.Sprintf("engine version changes from %s to %s", current.EngineVersion, s.version))
	}
	want := s.engineConfigs()
	configs := false
	for _, config := range []string{maxMemoryConfig, maxMemoryPolicyConfig} {
		if current.EngineConfigs[config] != want[config] {
			configs = true
			differences = append(differences, fmt.Sprintf("%s changes from %s to %s", config, cmp.Or(current.EngineConfigs[config], "unset"), want[config]))
		}
	}
	if configs {
		mask = append(mask, "engine_configs")
	}
	if replicas := replicasOf(current); replicas != s.replicas {
		differences = append(differences, fmt.Sprintf("replica count changes from %d to %d", replicas, s.replicas))
	}
	return mask, differences
}

func replicasOf(instance *gcloud.MemorystoreInstance) int {
	if instance.ReplicaCount == nil {
		return 0
	}
	return *instance.ReplicaCount
}

type kvInstance struct {
	id        string
	path      string
	storeName string
	service   gcloud.Memorystore
}

func (p *KV) openKVInstance(ctx context.Context, ref provider.StackRef, store string) (*gcloud.Clients, kvInstance, error) {
	clients, err := p.openClients(ctx)
	if err != nil {
		return nil, kvInstance{}, err
	}
	id, err := clients.KVInstance(ref.Project, ref.Name.Env, store)
	if err != nil {
		return nil, kvInstance{}, err
	}
	service, err := clients.Memorystore()
	if err != nil {
		return nil, kvInstance{}, err
	}
	return clients, kvInstance{id: id, path: clients.Location() + "/instances/" + id, storeName: store, service: service}, nil
}

func (p *KV) Provision(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	store, err := readKVStoreOn(in.Resource, in.Ref.Tier)
	if err != nil {
		return provider.Binding{}, err
	}
	clients, instance, err := p.openKVInstance(ctx, in.Ref, in.Resource.Name)
	if err != nil {
		return provider.Binding{}, err
	}
	labels, err := names.LabelsFor(clients.Names, in.Ref, names.KVLabel, in.Resource.Name)
	if err != nil {
		return provider.Binding{}, err
	}
	current, err := instance.service.ReadInstance(ctx, instance.path)
	switch {
	case gcloud.Absent(err):
		desired := store.instance(clients.Project(), clients.NetworkPath(in.Ref.Tier), labels)
		current, err = instance.create(ctx, clients, desired, progress)
	case err != nil:
		return provider.Binding{}, fmt.Errorf("read the Memorystore instance kv %s runs on: %w", in.Resource.Name, err)
	case gcloud.IsSettling(current.State):
		if current, err = instance.service.ReadSettledInstance(ctx, instance.path, in.Resource.Name); err == nil {
			if err = instance.refuseInactive(current, clients.Region); err == nil {
				current, err = instance.update(ctx, store, current, progress)
			}
		}
	case current.State != gcloud.InstanceActive:
		err = instance.refuseInactive(current, clients.Region)
	default:
		current, err = instance.update(ctx, store, current, progress)
	}
	if err != nil {
		return provider.Binding{}, err
	}
	return instance.readBinding(ctx, current, in.Resource)
}

func (p *KV) Plan(ctx context.Context, in resources.ProvisionRequest, _ *provider.Binding) (provider.ChangeAction, string, error) {
	store, err := readKVStoreOn(in.Resource, in.Ref.Tier)
	if err != nil {
		return "", "", err
	}
	clients, instance, err := p.openKVInstance(ctx, in.Ref, in.Resource.Name)
	if err != nil {
		return "", "", err
	}
	current, err := instance.service.ReadInstance(ctx, instance.path)
	switch {
	case gcloud.Absent(err):
		return provider.ActionCreate, "", nil
	case err != nil:
		return "", "", fmt.Errorf("read the Memorystore instance kv %s runs on: %w", in.Resource.Name, err)
	case gcloud.IsSettling(current.State):
		_, differences := store.changes(current)
		settling := "Memorystore instance " + instance.id + " is " + current.State +
			", so the deploy waits for it to settle and then applies any change to its node type, engine version, engine configs or replica count"
		return provider.ActionUpdate, strings.Join(append([]string{settling}, differences...), "; "), nil
	case current.State != gcloud.InstanceActive:
		return "", "", instance.refuseInactive(current, clients.Region)
	}
	if _, differences := store.changes(current); len(differences) > 0 {
		return provider.ActionUpdate, strings.Join(differences, "; "), nil
	}
	return provider.ActionKeep, provider.ReasonCurrent, nil
}

func (i kvInstance) create(ctx context.Context, clients *gcloud.Clients, desired *gcloud.MemorystoreInstance, progress progress.Log) (*gcloud.MemorystoreInstance, error) {
	gcloud.EnsureProgress(progress).Say("Creating kv " + i.storeName + " as Memorystore instance " + i.id + " in " + clients.Region +
		": a new instance takes several minutes")
	started, err := i.service.CreateInstance(ctx, clients.Location(), i.id, desired)
	if err != nil {
		return nil, fmt.Errorf("create the Memorystore instance kv %s runs on: %w", i.storeName, err)
	}
	finished, err := i.service.Awaited(ctx, "creating kv "+i.storeName, started)
	if err != nil {
		return nil, err
	}
	if timed := finished.Metadata; timed != nil {
		gcloud.EnsureProgress(progress).Say("Created kv " + i.storeName + ": its create operation " + finished.Name +
			" created " + timed.CreateTime + " and ended " + timed.EndTime)
	}
	return i.service.ReadInstance(ctx, i.path)
}

func (i kvInstance) update(ctx context.Context, store kvStore, current *gcloud.MemorystoreInstance, progress progress.Log) (*gcloud.MemorystoreInstance, error) {
	mask, differences := store.changes(current)
	if len(differences) == 0 {
		return current, nil
	}
	gcloud.EnsureProgress(progress).Say("Updating kv " + i.storeName + " on Memorystore instance " + i.id + ": " + strings.Join(differences, "; "))
	if len(mask) > 0 {
		if err := i.patch(ctx, mask, &gcloud.MemorystoreInstance{
			NodeType:      store.node.nodeType,
			EngineVersion: store.version,
			EngineConfigs: store.engineConfigs(),
		}); err != nil {
			return nil, err
		}
	}
	if replicasOf(current) != store.replicas {
		replicas := store.replicas
		if err := i.patch(ctx, []string{"replica_count"}, &gcloud.MemorystoreInstance{ReplicaCount: &replicas}); err != nil {
			return nil, err
		}
	}
	return i.service.ReadInstance(ctx, i.path)
}

func (i kvInstance) patch(ctx context.Context, mask []string, changed *gcloud.MemorystoreInstance) error {
	started, err := i.service.UpdateInstance(ctx, i.path, mask, changed)
	if err != nil {
		return fmt.Errorf("update the Memorystore instance kv %s runs on: %w", i.storeName, err)
	}
	_, err = i.service.Awaited(ctx, "updating kv "+i.storeName, started)
	return err
}

func (i kvInstance) refuseInactive(current *gcloud.MemorystoreInstance, region string) error {
	if current.State == gcloud.InstanceActive {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"kv %s runs on the Memorystore instance %s, which is %s, and only an instance Memorystore is creating or updating becomes %s by itself: "+
			"a store is bound and updated only once it is %s.\n"+
			"If it is being deleted, deploy again once it is gone. Otherwise delete it with `gcloud memorystore instances delete %s --location %s`, "+
			"which deletes the data it holds, and deploy again to create it afresh",
		i.storeName, i.id, current.State, gcloud.InstanceActive, gcloud.InstanceActive, i.id, region)
}

func (i kvInstance) readBinding(ctx context.Context, current *gcloud.MemorystoreInstance, resource provider.Resource) (provider.Binding, error) {
	host, port, err := primaryAddressOf(current, resource.Name)
	if err != nil {
		return provider.Binding{}, err
	}
	tokens, err := i.service.ListDefaultTokens(ctx, i.path)
	if err != nil {
		return provider.Binding{}, fmt.Errorf("read the token kv %s authenticates with: %w", resource.Name, err)
	}
	token, err := newestActiveTokenOf(tokens, resource.Name)
	if err != nil {
		return provider.Binding{}, err
	}
	authority, err := i.service.ReadCertificateAuthority(ctx, i.path)
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
			provider.PropertyUsername: gcloud.DefaultTokenUser,
			provider.PropertyPassword: token,
			provider.PropertyTLS:      "true",
			provider.PropertyCAPEM:    pem,
		},
	}, nil
}

func primaryAddressOf(instance *gcloud.MemorystoreInstance, store string) (string, int, error) {
	for _, endpoint := range instance.Endpoints {
		for _, connection := range endpoint.Connections {
			auto := connection.PSCAutoConnection
			if auto == nil || auto.IPAddress == "" || auto.ConnectionType != gcloud.PrimaryEndpoint {
				continue
			}
			return auto.IPAddress, cmp.Or(auto.Port, Port), nil
		}
	}
	return "", 0, refusal.Refuse(refusal.CodeNotReady,
		"the Memorystore instance kv %s runs on has no primary endpoint yet, so nothing names the address an app reaches it at", store)
}

func newestActiveTokenOf(tokens []gcloud.AuthToken, store string) (string, error) {
	var newest *gcloud.AuthToken
	for i := range tokens {
		if tokens[i].State != gcloud.TokenActive || tokens[i].Token == "" {
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

func authorityPEMOf(authority *gcloud.CertificateAuthority, store string) (string, error) {
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

func (p *KV) Remove(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	_, instance, err := p.openKVInstance(ctx, ref, binding.Name)
	if err != nil {
		return err
	}
	gcloud.EnsureProgress(progress).Say("Removing kv " + binding.Name + ", its Memorystore instance " + instance.id + " and its data")
	started, err := instance.service.DeleteInstance(ctx, instance.path)
	if gcloud.Absent(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete the Memorystore instance kv %s runs on: %w", binding.Name, err)
	}
	_, err = instance.service.Awaited(ctx, "deleting kv "+binding.Name, started)
	return err
}

type KV struct {
	openClients func(ctx context.Context) (*gcloud.Clients, error)
}

func New(openClients func(ctx context.Context) (*gcloud.Clients, error)) *KV {
	return &KV{openClients: openClients}
}

func (p *KV) Component() component.Component {
	return component.Component{
		Types:         []provider.BindingType{provider.BindingKV},
		Hooks:         resources.ResourceHooks{Plan: p.Plan, Provision: p.Provision, Remove: p.Remove},
		ListResources: p.ListResources,
		Egress:        true,
		Parts:         []component.Part{network.Part},
		Preflight: func(resource provider.Resource) error {
			_, err := readKVStore(resource)
			return err
		},
		Shape: shapeKV,
	}
}

func shapeKV(shape component.Shape, declared []provider.Resource) error {
	for _, resource := range declared {
		store, err := readKVStoreOn(resource, shape.Request.Deploy.Tier)
		if err != nil {
			return err
		}
		instance, err := shape.Names.KVInstance(shape.Request.Deploy.Slug, shape.Request.Deploy.Env, resource.Name)
		if err != nil {
			return err
		}
		shape.Tree.Add(shape.Environment, shape.Vendor, cost.MemorystoreInstanceType, instance, shape.Region, store.shapeProperties(shape.Region))
	}
	return nil
}
