package kv

import (
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

func shape(cost component.CostTree, scope string, at component.Stack, declared []provider.Resource) error {
	for _, resource := range declared {
		args, err := translateKV(resource.Name, resource.KV)
		if err != nil {
			return err
		}
		names := resourceNames(at.Project, at.Env, resource.Name)
		cost.Add(scope, component.TerraformElastiCacheReplicationGroup, resource.Name, map[string]any{
			"engine":                     args.Engine,
			"engine_version":             args.EngineVersion,
			"node_type":                  args.NodeType,
			"num_cache_clusters":         args.NumCacheClusters,
			"automatic_failover_enabled": args.AutomaticFailoverEnabled,
			"multi_az_enabled":           args.MultiAZEnabled,
			"snapshot_retention_limit":   args.SnapshotRetentionLimit,
		}, names["replicationGroup"])
		cost.Add(scope, component.TerraformElastiCacheParameterGroup, resource.Name, map[string]any{"family": args.Family}, names["parameterGroup"])
		cost.Add(scope, component.TerraformElastiCacheSubnetGroup, resource.Name, map[string]any{}, names["subnetGroup"])
		cost.AddPlain(scope, component.TerraformSSMParameter, resource.Name, map[string]any{"type": "SecureString"})
	}
	return nil
}
