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

func resourceNames(project, env, logicalName string) map[string]component.ResourceRef {
	at := component.ResourceCoordinate(project, env, logicalName, naming.KindKV)
	return map[string]component.ResourceRef{
		"securityGroup":    {Token: component.TokenEC2SecurityGroup, Name: naming.ResourceID(at.Kind, at.Name, "security-group")},
		"subnetGroup":      {Token: component.TokenElastiCacheSubnetGroup, Name: naming.ResourceID(at.Kind, at.Name, "subnet-group")},
		"parameterGroup":   {Token: component.TokenElastiCacheParameterGroup, Name: naming.ResourceID(at.Kind, at.Name, "parameters")},
		"replicationGroup": {Token: component.TokenElastiCacheReplicationGroup, Name: naming.ResourceID(at.Kind, at.Name)},
	}
}
