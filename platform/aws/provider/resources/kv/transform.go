package kv

import (
	"github.com/ocelhq/ocel/pkg/naming"
	ocelpulumi "github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

func checkKVNodeCount(logicalName string, patch map[string]any) error {
	nodes, counted := patch["numCacheClusters"].(float64)
	if !counted || nodes >= kvNodeCount {
		return nil
	}
	if patch["automaticFailoverEnabled"] == false && patch["multiAzEnabled"] == false {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"a transform runs %s on %v node, and ElastiCache refuses automaticFailoverEnabled and multiAzEnabled without a replica; set both to false in the same patch",
		logicalName, nodes)
}

type Names struct {
	Group         string
	Subnets       string
	Parameters    string
	SecurityGroup string
}

func NamesFor(at component.Stack, logicalName, family string) (Names, error) {
	group, err := component.PhysicalName(at.Namespace, component.ResourceCoordinate(at.Project, at.Env, logicalName, naming.KindKV), maxReplicationGroupIDLen)
	if err != nil {
		return Names{}, err
	}
	return Names{
		Group:         group,
		Subnets:       group + naming.WordSeparator + subnetsSuffix,
		Parameters:    group + naming.WordSeparator + family,
		SecurityGroup: component.SecurityGroupName(group),
	}, nil
}

func (n Names) Surfaces() map[string][]ocelpulumi.ResourceRef {
	return map[string][]ocelpulumi.ResourceRef{
		"securityGroup":    {n.securityGroup()},
		"subnetGroup":      {n.subnetGroup()},
		"parameterGroup":   {n.parameterGroup()},
		"replicationGroup": {n.replicationGroup()},
	}
}

func (n Names) securityGroup() ocelpulumi.ResourceRef {
	return ocelpulumi.ResourceRef{Token: component.TokenEC2SecurityGroup, Name: n.SecurityGroup}
}

func (n Names) subnetGroup() ocelpulumi.ResourceRef {
	return ocelpulumi.ResourceRef{Token: component.TokenElastiCacheSubnetGroup, Name: n.Subnets}
}

func (n Names) parameterGroup() ocelpulumi.ResourceRef {
	return ocelpulumi.ResourceRef{Token: component.TokenElastiCacheParameterGroup, Name: n.Parameters}
}

func (n Names) replicationGroup() ocelpulumi.ResourceRef {
	return ocelpulumi.ResourceRef{Token: component.TokenElastiCacheReplicationGroup, Name: n.Group}
}
