package deploy

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/kvstore"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	mib = int64(1) << 20
	gib = int64(1) << 30
)

func TestAStoreGetsTheCheapestNodeThatHoldsItsMemoryWithTheReserveThatNodeKeeps(t *testing.T) {
	t.Parallel()

	cases := []struct {
		memory  int64
		node    string
		reserve int
	}{
		{32 * mib, "cache.t4g.micro", 94},
		{256 * mib, "cache.t4g.micro", 50},
		{256*mib + 1, "cache.t4g.small", 82},
		{512 * mib, "cache.t4g.small", 64},
		{gib, "cache.t4g.medium", 68},
		{2 * gib, "cache.t4g.medium", 36},
		{2*gib + 400*mib, "cache.m7g.large", 63},
		{4 * gib, "cache.m7g.large", 38},
		{5 * gib, "cache.r7g.large", 62},
		{9 * gib, "cache.r7g.large", 32},
		{10 * gib, "cache.r7g.xlarge", 63},
		{19 * gib, "cache.r7g.xlarge", 28},
		{20 * gib, "cache.r7g.2xlarge", 63},
		{32 * gib, "cache.r7g.2xlarge", 40},
	}
	for _, tc := range cases {
		got, err := translateKV("cache", &provider.KVSpec{MemoryBytes: tc.memory})
		if err != nil {
			t.Fatalf("translateKV(%d bytes) = %v", tc.memory, err)
		}
		if got.NodeType != tc.node || got.ReservedMemoryPercent != tc.reserve {
			t.Errorf("translateKV(%d bytes) = %s reserving %d%%, want %s reserving %d%%",
				tc.memory, got.NodeType, got.ReservedMemoryPercent, tc.node, tc.reserve)
		}
	}
}

func TestEveryNodeInTheLadderHoldsWhatTheContractAllows(t *testing.T) {
	t.Parallel()

	for _, memory := range []int64{kvstore.MinMemoryBytes, kvstore.MaxMemoryBytes} {
		if _, err := translateKV("cache", &provider.KVSpec{MemoryBytes: memory}); err != nil {
			t.Errorf("translateKV(%d bytes) = %v, want a node for every size a store may declare", memory, err)
		}
	}
	if _, err := translateKV("cache", &provider.KVSpec{MemoryBytes: kvstore.MaxMemoryBytes * 2}); err == nil {
		t.Error("translateKV(64gb) found a node, want it refused: no node in the ladder holds it")
	}
}

func TestAStoreDeclaringNothingRunsTwoNodesOfTheDefaultVersionThatNeverEvict(t *testing.T) {
	t.Parallel()

	got, err := translateKV("cache", nil)
	if err != nil {
		t.Fatalf("translateKV(nil) = %v", err)
	}
	want := kvArgs{
		Engine:                   "valkey",
		EngineVersion:            "9.1",
		Family:                   "valkey9",
		NodeType:                 "cache.t4g.micro",
		ReservedMemoryPercent:    50,
		MaxMemoryPolicy:          "noeviction",
		Port:                     6379,
		NumCacheClusters:         2,
		AutomaticFailoverEnabled: true,
		MultiAZEnabled:           true,
		TransitEncryptionEnabled: true,
		AtRestEncryptionEnabled:  true,
		SnapshotRetentionLimit:   1,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("translateKV(nil) = %+v\nwant %+v", got, want)
	}
}

func TestAStoreEvictsByThePolicyItDeclares(t *testing.T) {
	t.Parallel()

	got, err := translateKV("cache", &provider.KVSpec{Eviction: "allkeys-lru"})
	if err != nil {
		t.Fatalf("translateKV() = %v", err)
	}
	if got.MaxMemoryPolicy != "allkeys-lru" {
		t.Errorf("MaxMemoryPolicy = %q, want allkeys-lru", got.MaxMemoryPolicy)
	}
}

func TestAStoreRunsTheElastiCacheMinorOfItsMajor(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ version, family string }{
		"8": {"8.1", "valkey8"},
		"9": {"9.1", "valkey9"},
	}
	for major, want := range cases {
		got, err := translateKV("cache", &provider.KVSpec{Version: major})
		if err != nil {
			t.Fatalf("translateKV(version %s) = %v", major, err)
		}
		if got.EngineVersion != want.version || got.Family != want.family {
			t.Errorf("translateKV(version %s) = %s in %s, want %s in %s", major, got.EngineVersion, got.Family, want.version, want.family)
		}
	}

	_, err := translateKV("cache", &provider.KVSpec{Version: "7"})
	if err == nil || !strings.Contains(err.Error(), "cache") || !strings.Contains(err.Error(), "8, 9") {
		t.Errorf("translateKV(version 7) = %v, want it refused naming the store and the versions it runs", err)
	}
}

func TestAStoreOnElastiCacheRunsTheMinorAStoreOnABoxRuns(t *testing.T) {
	t.Parallel()

	for _, major := range images.ValkeyVersions() {
		image, _ := images.Valkey(major)
		tag := image[strings.Index(image, ":")+1 : strings.Index(image, "@")]
		boxMinor := strings.Join(strings.Split(tag, ".")[:2], ".")

		got, err := translateKV("cache", &provider.KVSpec{Version: major})
		if err != nil {
			t.Fatalf("translateKV(version %s) = %v, want every version a box runs to run here", major, err)
		}
		if got.EngineVersion != boxMinor {
			t.Errorf("version %s runs %s on ElastiCache and %s on a box, want the same minor on both", major, got.EngineVersion, boxMinor)
		}
	}
}

func registeredKV(t *testing.T, project string) *tagRecorder {
	t.Helper()
	args, err := translateKV("cache", nil)
	if err != nil {
		t.Fatalf("translateKV() = %v", err)
	}
	args.AuthToken = "a-minted-token"
	args.AuthTokenParameter = "/ocel/kv/shop/prod/kv--cache"
	return recordTags(t, func(ctx *pulumi.Context) error {
		return registerKV(ctx, project, "prod", "kv--cache", args, "vpc-1", "10.0.0.0/16", []string{"subnet-1", "subnet-2"})
	})
}

func TestAStoreIsAReplicationGroupOfTwoEncryptedNodesThatFailOverAcrossZones(t *testing.T) {
	t.Parallel()

	group := registeredKV(t, "shop").inputsOf(t, tokenElastiCacheReplicationGroup, "kv-cache")

	for field, want := range map[string]any{
		"replicationGroupId":       "ocel-app-shop-prod-cache",
		"engine":                   "valkey",
		"engineVersion":            "9.1",
		"nodeType":                 "cache.t4g.micro",
		"port":                     float64(6379),
		"numCacheClusters":         float64(2),
		"automaticFailoverEnabled": true,
		"multiAzEnabled":           true,
		"transitEncryptionEnabled": true,
		"atRestEncryptionEnabled":  true,
		"snapshotRetentionLimit":   float64(1),
		"parameterGroupName":       "ocel-app-shop-prod-cache-valkey9",
		"subnetGroupName":          "ocel-app-shop-prod-cache-subnets",
	} {
		if got := group[resource.PropertyKey(field)].V; got != want {
			t.Errorf("replication group %s = %v, want %v", field, got, want)
		}
	}
	token := group["authToken"]
	if !token.IsSecret() || token.SecretValue().Element.StringValue() != "a-minted-token" {
		t.Errorf("replication group authToken = %v, want the minted token held as a secret", token)
	}
}

func TestAStoresParameterGroupCarriesItsEvictionAndReserveUnderAFamilyNamedGroup(t *testing.T) {
	t.Parallel()

	group := registeredKV(t, "shop").inputsOf(t, tokenElastiCacheParameterGroup, "kv-cache-parameters")

	if got := group["family"].StringValue(); got != "valkey9" {
		t.Errorf("parameter group family = %q, want valkey9", got)
	}
	if got := group["name"].StringValue(); got != "ocel-app-shop-prod-cache-valkey9" {
		t.Errorf("parameter group name = %q, want the family in it so a major upgrade makes a new group", got)
	}
	parameters := map[string]string{}
	for _, parameter := range group["parameters"].ArrayValue() {
		fields := parameter.ObjectValue()
		parameters[fields["name"].StringValue()] = fields["value"].StringValue()
	}
	want := map[string]string{"maxmemory-policy": "noeviction", "reserved-memory-percent": "50"}
	if !reflect.DeepEqual(parameters, want) {
		t.Errorf("parameter group parameters = %v, want %v", parameters, want)
	}
}

func TestAStoreAdmitsItsPortFromTheVPCAlone(t *testing.T) {
	t.Parallel()

	sg := registeredKV(t, "shop").inputsOf(t, tokenEC2SecurityGroup, "kv-cache-security-group")

	ingress := sg["ingress"].ArrayValue()
	if len(ingress) != 1 {
		t.Fatalf("security group has %d ingress rules, want the one admitting the store's port", len(ingress))
	}
	rule := ingress[0].ObjectValue()
	if rule["fromPort"].NumberValue() != 6379 || rule["toPort"].NumberValue() != 6379 {
		t.Errorf("ingress admits %v-%v, want 6379", rule["fromPort"], rule["toPort"])
	}
	cidrs := rule["cidrBlocks"].ArrayValue()
	if len(cidrs) != 1 || cidrs[0].StringValue() != "10.0.0.0/16" {
		t.Errorf("ingress admits %v, want the VPC's CIDR alone", cidrs)
	}
}

func TestAStoreSendsNothingOutOfItsSecurityGroup(t *testing.T) {
	t.Parallel()

	group := registeredKV(t, "shop").inputsOf(t, tokenEC2SecurityGroup, "kv-cache-security-group")

	if egress, set := group["egress"]; !set || !egress.IsArray() || len(egress.ArrayValue()) != 0 {
		t.Errorf("security group egress = %v, want an empty list: ElastiCache opens no connection out, and an empty list also revokes the allow-all rule AWS adds", egress)
	}
}

func TestEveryResourceOfAStoreIsTaggedAsAKVStore(t *testing.T) {
	t.Parallel()

	rec := registeredKV(t, "shop")
	for typeToken, name := range map[string]string{
		tokenEC2SecurityGroup:            "kv-cache-security-group",
		tokenElastiCacheSubnetGroup:      "kv-cache-subnet-group",
		tokenElastiCacheParameterGroup:   "kv-cache-parameters",
		tokenElastiCacheReplicationGroup: "kv-cache",
	} {
		if got, want := rec.component(t, typeToken, name), naming.KindKV.Component(); got != want {
			t.Errorf("%s on %s = %q, want %q", tagComponent, name, got, want)
		}
	}
}

func TestEveryResourceOfAStoreIsTaggedWithTheStoresDeclaredName(t *testing.T) {
	t.Parallel()

	rec := registeredKV(t, strings.Repeat("a-long-project-slug-", 3))
	for typeToken, name := range map[string]string{
		tokenEC2SecurityGroup:            "kv-cache-security-group",
		tokenElastiCacheSubnetGroup:      "kv-cache-subnet-group",
		tokenElastiCacheParameterGroup:   "kv-cache-parameters",
		tokenElastiCacheReplicationGroup: "kv-cache",
	} {
		rec.mu.Lock()
		got := rec.tags[typeToken+"::"+name][tagResource]
		rec.mu.Unlock()
		if got != "cache" {
			t.Errorf("%s on %s = %q, want cache: a long project slug compresses the store's name out of its ids, and this tag still finds it", tagResource, name, got)
		}
	}
}
