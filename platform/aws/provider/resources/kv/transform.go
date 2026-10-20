package kv

import (
	"github.com/ocelhq/ocel/pkg/naming"
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

func resourceNames(at component.Stack, logicalName, family string) (map[string][]component.ResourceRef, error) {
	group, err := groupID(at.Namespace, component.ResourceCoordinate(at.Project, at.Env, logicalName, naming.KindKV))
	if err != nil {
		return nil, err
	}
	return map[string][]component.ResourceRef{
		"securityGroup":    {{Token: component.TokenEC2SecurityGroup, Name: component.SecurityGroupName(group)}},
		"subnetGroup":      {{Token: component.TokenElastiCacheSubnetGroup, Name: group + naming.WordSeparator + subnetsSuffix}},
		"parameterGroup":   {{Token: component.TokenElastiCacheParameterGroup, Name: group + naming.WordSeparator + family}},
		"replicationGroup": {{Token: component.TokenElastiCacheReplicationGroup, Name: group}},
	}, nil
}
