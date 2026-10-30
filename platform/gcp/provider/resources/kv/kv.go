package kv

import (
	"cmp"
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/valkey"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
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

func (s kvStore) pricedProperties(region string, instance *gcloud.MemorystoreInstance) map[string]any {
	return map[string]any{
		"location":           region,
		"mode":               clusterModeOff,
		"node_type":          s.node.terraformNodeType(),
		"node_capacity_gb":   s.node.capacityGB,
		"shard_count":        1,
		"replica_count":      replicasOf(instance),
		"persistence_config": map[string]any{"mode": appendOnly, "aof_config": map[string]any{"append_fsync": fsyncEverySecond}},
	}
}

type declaredStore struct {
	name    string
	patched surface.RecordedPatch[gcloud.MemorystoreInstance, instanceUpdate]
}

func (p *KV) declare(ctx context.Context, clients *gcloud.Clients, in resources.ProvisionRequest, store kvStore) (declaredStore, error) {
	labels, err := names.LabelsFor(clients.Names, in.Ref, names.KVLabel, in.Resource.Name)
	if err != nil {
		return declaredStore{}, err
	}
	evaluated, _ := surface.ReadEvaluated(ctx, in.Ref)
	result := evaluated.For(transformKind, in.Resource.Name)
	record := surface.Record{Store: p.records, Key: surface.KeyFor(in.Ref, transformKind, in.Resource.Name)}
	own := store.instance(clients.Project(), clients.NetworkPath(in.Ref.Tier), labels)
	patched, err := storeSurface.PatchRecorded(ctx, record, "kv "+in.Resource.Name, *own, result)
	return declaredStore{name: in.Resource.Name, patched: patched}, err
}

func (d declaredStore) desired() *gcloud.MemorystoreInstance { return &d.patched.Desired }

func (d declaredStore) network() string { return connectionOf(d.desired()).Network }

type storeChanges struct {
	engine      []string
	replicas    bool
	patched     instanceUpdate
	differences []string
}

func (d declaredStore) drift(current *gcloud.MemorystoreInstance) (storeChanges, error) {
	if err := d.refuseZonesChange(current); err != nil {
		return storeChanges{}, err
	}
	desired := d.desired()
	changes := storeChanges{patched: instanceUpdate{currentLabels: current.Labels}}
	if current.NodeType != desired.NodeType {
		changes.engine = append(changes.engine, "node_type")
		changes.differences = append(changes.differences, fmt.Sprintf("node type changes from %s to %s", current.NodeType, desired.NodeType))
	}
	if current.EngineVersion != desired.EngineVersion {
		changes.engine = append(changes.engine, "engine_version")
		changes.differences = append(changes.differences, fmt.Sprintf("engine version changes from %s to %s", current.EngineVersion, desired.EngineVersion))
	}
	configs := false
	for _, config := range []string{maxMemoryConfig, maxMemoryPolicyConfig} {
		if current.EngineConfigs[config] != desired.EngineConfigs[config] {
			configs = true
			changes.differences = append(changes.differences, fmt.Sprintf("%s changes from %s to %s", config, cmp.Or(current.EngineConfigs[config], "unset"), desired.EngineConfigs[config]))
		}
	}
	if configs {
		changes.engine = append(changes.engine, "engine_configs")
	}
	if held, wanted := replicasOf(current), replicasOf(desired); held != wanted {
		changes.replicas = true
		changes.differences = append(changes.differences, fmt.Sprintf("replica count changes from %d to %d", held, wanted))
	}
	patched, err := d.patched.Drift(current, &changes.patched)
	if err != nil {
		return storeChanges{}, err
	}
	changes.differences = append(changes.differences, patched...)
	return changes, nil
}

func (d declaredStore) refuseZonesChange(current *gcloud.MemorystoreInstance) error {
	held, wanted := zonesOf(current), zonesOf(d.desired())
	if held.Mode == wanted.Mode && (wanted.Zone == "" || held.Zone == wanted.Zone) {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"a transform sets kv %s's %s.%s to %s, and its Memorystore instance keeps the %s it was made with: patch it back to %s",
		d.name, transformSurface, zonesPath, describeZones(wanted), describeZones(held), describeZones(held))
}

func (d declaredStore) refuseEndpointMove(instance kvInstance, current *gcloud.MemorystoreInstance) error {
	return gcloud.RefuseEndpointMove("kv "+d.name, "Memorystore instance "+instance.id, gcloud.StoreNetworks(current), d.network())
}

func (p *KV) refusePlacedNetworkAbsent(ctx context.Context, clients *gcloud.Clients, declared declaredStore) error {
	if p.skipChecks || !declared.patched.Sets(surface.StorePlacement) {
		return nil
	}
	return gcloud.RefuseNetworkAbsent(ctx, clients, "kv "+declared.name, declared.network())
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
	declared, err := p.declare(ctx, clients, in, store)
	if err != nil {
		return provider.Binding{}, err
	}
	var current *gcloud.MemorystoreInstance
	err = declared.patched.Apply(ctx, func() error {
		current, err = instance.service.ReadInstance(ctx, instance.path)
		switch {
		case gcloud.Absent(err):
			if err = p.refusePlacedNetworkAbsent(ctx, clients, declared); err == nil {
				current, err = instance.create(ctx, clients, declared.desired(), progress)
			}
		case err != nil:
			err = fmt.Errorf("read the Memorystore instance kv %s runs on: %w", in.Resource.Name, err)
		case gcloud.IsSettling(current.State):
			if current, err = instance.service.ReadSettledInstance(ctx, instance.path, in.Resource.Name); err == nil {
				if err = instance.refuseInactive(current, clients.Region); err == nil {
					current, err = instance.update(ctx, declared, current, progress)
				}
			}
		case current.State != gcloud.InstanceActive:
			err = instance.refuseInactive(current, clients.Region)
		default:
			current, err = instance.update(ctx, declared, current, progress)
		}
		return err
	})
	if err != nil {
		return provider.Binding{}, err
	}
	return instance.readBinding(ctx, current, in.Resource, declared.network(), clients.Region)
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
	declared, err := p.declare(ctx, clients, in, store)
	if err != nil {
		return "", "", err
	}
	current, err := instance.service.ReadInstance(ctx, instance.path)
	switch {
	case gcloud.Absent(err):
		return provider.ActionCreate, "", p.refusePlacedNetworkAbsent(ctx, clients, declared)
	case err != nil:
		return "", "", fmt.Errorf("read the Memorystore instance kv %s runs on: %w", in.Resource.Name, err)
	case current.State != gcloud.InstanceActive && !gcloud.IsSettling(current.State):
		return "", "", instance.refuseInactive(current, clients.Region)
	}
	if err := declared.refuseEndpointMove(instance, current); err != nil {
		return "", "", err
	}
	changes, err := declared.drift(current)
	if err != nil {
		return "", "", err
	}
	if gcloud.IsSettling(current.State) {
		settling := "Memorystore instance " + instance.id + " is " + current.State + ", so the deploy waits for it to settle and then applies any change"
		return provider.ActionUpdate, strings.Join(append([]string{settling}, changes.differences...), "; "), nil
	}
	if len(changes.differences) > 0 {
		return provider.ActionUpdate, strings.Join(changes.differences, "; "), nil
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

func (i kvInstance) update(ctx context.Context, declared declaredStore, current *gcloud.MemorystoreInstance, progress progress.Log) (*gcloud.MemorystoreInstance, error) {
	if err := declared.refuseEndpointMove(i, current); err != nil {
		return nil, err
	}
	changes, err := declared.drift(current)
	if err != nil || len(changes.differences) == 0 {
		return current, err
	}
	gcloud.EnsureProgress(progress).Say("Updating kv " + i.storeName + " on Memorystore instance " + i.id + ": " + strings.Join(changes.differences, "; "))
	desired := declared.desired()
	if len(changes.engine) > 0 {
		if err := i.patch(ctx, changes.engine, &gcloud.MemorystoreInstance{
			NodeType:      desired.NodeType,
			EngineVersion: desired.EngineVersion,
			EngineConfigs: desired.EngineConfigs,
		}); err != nil {
			return nil, err
		}
	}
	if changes.replicas {
		replicas := replicasOf(desired)
		if err := i.patch(ctx, []string{"replica_count"}, &gcloud.MemorystoreInstance{ReplicaCount: &replicas}); err != nil {
			return nil, err
		}
	}
	if len(changes.patched.mask) > 0 {
		if err := i.patch(ctx, changes.patched.mask, &changes.patched.changed); err != nil {
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

func (i kvInstance) readBinding(ctx context.Context, current *gcloud.MemorystoreInstance, resource provider.Resource, network, region string) (provider.Binding, error) {
	host, port, err := primaryAddressOf(current, resource.Name, network, region)
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

func primaryAddressOf(instance *gcloud.MemorystoreInstance, store, network, region string) (string, int, error) {
	for _, endpoint := range instance.Endpoints {
		for _, connection := range endpoint.Connections {
			auto := connection.PSCAutoConnection
			if auto == nil || auto.IPAddress == "" || auto.ConnectionType != gcloud.PrimaryEndpoint || auto.Network != "" && auto.Network != network {
				continue
			}
			return auto.IPAddress, cmp.Or(auto.Port, Port), nil
		}
	}
	return "", 0, refusal.Refuse(refusal.CodeNotReady,
		"the Memorystore instance kv %s runs on has no primary endpoint on the %s network yet, so nothing names the address an app reaches it at.\n"+
			"Check that the %s network has a Memorystore service connection policy in %s, then deploy again",
		store, path.Base(network), path.Base(network), region)
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
	patches := surface.Record{Store: p.records, Key: surface.KeyFor(ref, transformKind, binding.Name)}
	if err := instance.unprotect(ctx); err != nil {
		return err
	}
	started, err := instance.service.DeleteInstance(ctx, instance.path)
	if gcloud.Absent(err) {
		return patches.Forget(ctx)
	}
	if err != nil {
		return fmt.Errorf("delete the Memorystore instance kv %s runs on: %w", binding.Name, err)
	}
	if _, err := instance.service.Awaited(ctx, "deleting kv "+binding.Name, started); err != nil {
		return err
	}
	return patches.Forget(ctx)
}

func (i kvInstance) unprotect(ctx context.Context) error {
	current, err := i.service.ReadInstance(ctx, i.path)
	switch {
	case gcloud.Absent(err):
		return nil
	case err != nil:
		return fmt.Errorf("read the Memorystore instance kv %s runs on: %w", i.storeName, err)
	case !isProtected(current):
		return nil
	}
	off := false
	return i.patch(ctx, []string{"deletion_protection_enabled"}, &gcloud.MemorystoreInstance{DeletionProtectionEnabled: &off})
}

type KV struct {
	openClients func(ctx context.Context) (*gcloud.Clients, error)
	records     records.Store
	skipChecks  bool
}

func New(openClients func(ctx context.Context) (*gcloud.Clients, error), store records.Store, skipChecks bool) *KV {
	return &KV{openClients: openClients, records: store, skipChecks: skipChecks}
}

var bindingTypes = []provider.BindingType{provider.BindingKV}

func (p *KV) Listing() component.Component {
	return component.Component{Types: bindingTypes, ListResources: p.ListResources}
}

func (p *KV) Component() component.Component {
	return component.Component{
		Types:         bindingTypes,
		Hooks:         resources.ResourceHooks{Plan: p.Plan, Provision: p.Provision, Remove: p.Remove},
		ListResources: p.ListResources,
		Egress:        true,
		Parts:         []component.Part{network.Part},
		Preflight: func(resource provider.Resource) error {
			_, err := readKVStore(resource)
			return err
		},
		Inventory:  inventoryKV,
		Transforms: map[provider.BindingType]*component.Transform{provider.BindingKV: transformTarget()},
	}
}

func inventoryKV(inventory component.Inventory, declared []provider.Resource) error {
	tier := inventory.Request.Deploy.Tier
	for _, resource := range declared {
		store, err := readKVStoreOn(resource, tier)
		if err != nil {
			return err
		}
		instance, err := inventory.Names.KVInstance(inventory.Request.Deploy.Slug, inventory.Request.Deploy.Env, resource.Name)
		if err != nil {
			return err
		}
		result := inventory.Transforms.For(transformKind, resource.Name)
		known, unknown := storeSurface.SplitOutputs(result.Patches[transformSurface])
		patched, err := storeSurface.Patch("kv "+resource.Name, *store.instance(inventory.Names.Project(), inventory.Names.NetworkPath(tier), nil), known, nil, surface.Recorded{})
		if err != nil {
			return err
		}
		inventory.Tree.Add(inventory.Environment, inventory.Vendor, cost.MemorystoreInstanceType, instance, inventory.Region, store.pricedProperties(inventory.Region, &patched.Desired),
			surface.ListPriced(unknown, pricedPaths)...)
	}
	return nil
}

var pricedPaths = map[string]string{"replicaCount": "replica_count"}
