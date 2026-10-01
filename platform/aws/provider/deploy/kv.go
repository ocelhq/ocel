package deploy

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
	"strings"

	ec2 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/elasticache"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/kvstore"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	kvEngine                 = "valkey"
	kvNodeCount              = 2
	kvSnapshotRetentionDays  = 1
	kvDefaultMaxMemoryPolicy = "noeviction"

	maxReplicationGroupIDLen          = 40
	maxReplicationGroupDescriptionLen = 255
	maxParameterGroupDescriptionLen   = 255
	outputKeyAuthTokenParameter       = "authTokenParameter"
)

var kvEngineVersions = map[string]string{
	"8": "8.1",
	"9": "9.1",
}

type kvNode struct {
	Type                string
	MemoryHundredthsGiB int64
	MinReservePercent   int
}

var kvNodeLadder = []kvNode{
	{Type: "cache.t4g.micro", MemoryHundredthsGiB: 50, MinReservePercent: 50},
	{Type: "cache.t4g.small", MemoryHundredthsGiB: 137, MinReservePercent: 30},
	{Type: "cache.t4g.medium", MemoryHundredthsGiB: 309, MinReservePercent: 25},
	{Type: "cache.m7g.large", MemoryHundredthsGiB: 638, MinReservePercent: 25},
	{Type: "cache.r7g.large", MemoryHundredthsGiB: 1307, MinReservePercent: 25},
	{Type: "cache.r7g.xlarge", MemoryHundredthsGiB: 2632, MinReservePercent: 25},
	{Type: "cache.r7g.2xlarge", MemoryHundredthsGiB: 5282, MinReservePercent: 25},
}

type kvArgs struct {
	Engine                   string
	EngineVersion            string
	Family                   string
	NodeType                 string
	ReservedMemoryPercent    int
	MaxMemoryPolicy          string
	Port                     int
	NumCacheClusters         int
	AutomaticFailoverEnabled bool
	MultiAZEnabled           bool
	TransitEncryptionEnabled bool
	AtRestEncryptionEnabled  bool
	SnapshotRetentionLimit   int

	AuthToken          string
	AuthTokenParameter string
	Tags               map[string]string
}

func translateKV(name string, spec *provider.KVSpec) (kvArgs, error) {
	declared := provider.KVSpec{}
	if spec != nil {
		declared = *spec
	}
	major := cmp.Or(declared.Version, kvstore.DefaultVersion)
	version, runs := kvEngineVersions[major]
	if !runs {
		return kvArgs{}, refusal.Refuse(refusal.CodeInvalid,
			"kv %s asks for version %q; this provider runs %s", name, major, strings.Join(slices.Sorted(maps.Keys(kvEngineVersions)), ", "))
	}
	memory := declared.MemoryBytes
	if memory == 0 {
		memory, _ = kvstore.ParseMemory(kvstore.DefaultMemory)
	}
	node, reserve, fits := kvNodeFor(memory)
	if !fits {
		return kvArgs{}, refusal.Refuse(refusal.CodeInvalid,
			"kv %s asks for %d bytes of memory, and the largest node this provider runs, %s, holds less", name, memory, kvNodeLadder[len(kvNodeLadder)-1].Type)
	}
	return kvArgs{
		Engine:                   kvEngine,
		EngineVersion:            version,
		Family:                   kvEngine + major,
		NodeType:                 node,
		ReservedMemoryPercent:    reserve,
		MaxMemoryPolicy:          cmp.Or(declared.Eviction, kvDefaultMaxMemoryPolicy),
		Port:                     kvstore.ValkeyPort,
		NumCacheClusters:         kvNodeCount,
		AutomaticFailoverEnabled: true,
		MultiAZEnabled:           true,
		TransitEncryptionEnabled: true,
		AtRestEncryptionEnabled:  true,
		SnapshotRetentionLimit:   kvSnapshotRetentionDays,
	}, nil
}

func kvGroupID(at naming.Coordinate) string {
	scope := awsports.AppScope + naming.WordSeparator
	return scope + naming.Fit(maxReplicationGroupIDLen-len(scope), naming.WordSeparator,
		naming.Fixed(at.Project),
		naming.Fixed(at.Env),
		naming.Compressible(at.Name),
	)
}

func registerKV(ctx *pulumi.Context, project, env, logicalName string, args kvArgs, vpcID, vpcCIDR string, subnetIDs []string) error {
	at := resourceCoordinate(project, env, logicalName, naming.KindKV)
	groupID := kvGroupID(at)
	tags := resourceTags(at.Kind, "", args.Tags)
	tags[tagResource] = pulumi.String(at.Name)

	securityGroup, err := newVPCSecurityGroup(ctx, at, vpcSecurityGroup{
		Subject: "the " + at.Name + " kv store",
		Engine:  "Valkey",
		Port:    args.Port,
		VPCID:   vpcID,
		VPCCIDR: vpcCIDR,
		Egress:  ec2.SecurityGroupEgressArray{},
		Tags:    tags,
	})
	if err != nil {
		return err
	}

	subnetGroup, err := elasticache.NewSubnetGroup(ctx, naming.ResourceID(at.Kind, at.Name, "subnet-group"), &elasticache.SubnetGroupArgs{
		Name:        pulumi.String(groupID + naming.WordSeparator + "subnets"),
		Description: capDescription(at.Description("subnet group placing the "+at.Name+" kv store in the VPC's subnets"), maxSubnetGroupDescriptionLen),
		SubnetIds:   pulumi.ToStringArray(subnetIDs),
		Tags:        tags,
	})
	if err != nil {
		return err
	}

	parameters, err := elasticache.NewParameterGroup(ctx, naming.ResourceID(at.Kind, at.Name, "parameters"), &elasticache.ParameterGroupArgs{
		Name:        pulumi.String(groupID + naming.WordSeparator + args.Family),
		Family:      pulumi.String(args.Family),
		Description: capDescription(at.Description("eviction and reserved memory for the "+at.Name+" kv store"), maxParameterGroupDescriptionLen),
		Parameters: elasticache.ParameterGroupParameterArray{
			&elasticache.ParameterGroupParameterArgs{Name: pulumi.String("maxmemory-policy"), Value: pulumi.String(args.MaxMemoryPolicy)},
			&elasticache.ParameterGroupParameterArgs{Name: pulumi.String("reserved-memory-percent"), Value: pulumi.String(strconv.Itoa(args.ReservedMemoryPercent))},
		},
		Tags: tags,
	})
	if err != nil {
		return err
	}

	group, err := elasticache.NewReplicationGroup(ctx, naming.ResourceID(at.Kind, at.Name), &elasticache.ReplicationGroupArgs{
		ReplicationGroupId:       pulumi.String(groupID),
		Description:              capDescription(at.Description("the "+at.Name+" kv store"), maxReplicationGroupDescriptionLen),
		Engine:                   pulumi.String(args.Engine),
		EngineVersion:            pulumi.String(args.EngineVersion),
		NodeType:                 pulumi.String(args.NodeType),
		Port:                     pulumi.Int(args.Port),
		NumCacheClusters:         pulumi.Int(args.NumCacheClusters),
		AutomaticFailoverEnabled: pulumi.Bool(args.AutomaticFailoverEnabled),
		MultiAzEnabled:           pulumi.Bool(args.MultiAZEnabled),
		TransitEncryptionEnabled: pulumi.Bool(args.TransitEncryptionEnabled),
		AtRestEncryptionEnabled:  pulumi.Bool(args.AtRestEncryptionEnabled),
		AuthToken:                pulumi.ToSecret(pulumi.String(args.AuthToken)).(pulumi.StringOutput),
		SnapshotRetentionLimit:   pulumi.Int(args.SnapshotRetentionLimit),
		ParameterGroupName:       parameters.Name,
		SubnetGroupName:          subnetGroup.Name,
		SecurityGroupIds:         pulumi.StringArray{securityGroup.ID()},
		ApplyImmediately:         pulumi.Bool(true),
		Tags:                     tags,
	}, pulumi.DeleteBeforeReplace(true))
	if err != nil {
		return err
	}

	ctx.Export(logicalName, pulumi.Map{
		outputKeyHost:               group.PrimaryEndpointAddress,
		outputKeyPort:               group.Port,
		outputKeyAuthTokenParameter: pulumi.String(args.AuthTokenParameter),
	})
	return nil
}

func kvNodeFor(memoryBytes int64) (string, int, bool) {
	const gib = int64(1) << 30
	for _, node := range kvNodeLadder {
		advertised := node.MemoryHundredthsGiB * gib
		if advertised*int64(100-node.MinReservePercent) < memoryBytes*100*100 {
			continue
		}
		held := int(memoryBytes * 100 * 100 / advertised)
		return node.Type, max(node.MinReservePercent, 100-held), true
	}
	return "", 0, false
}
