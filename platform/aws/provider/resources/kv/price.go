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
		names, err := resourceNames(at, resource.Name, args.Family)
		if err != nil {
			return err
		}
		cost.Add(scope, component.TerraformElastiCacheReplicationGroup, resource.Name, map[string]any{
			"engine":                     args.Engine,
			"engine_version":             args.EngineVersion,
			"node_type":                  args.NodeType,
			"num_cache_clusters":         args.NumCacheClusters,
			"automatic_failover_enabled": args.AutomaticFailoverEnabled,
			"multi_az_enabled":           args.MultiAZEnabled,
			"snapshot_retention_limit":   args.SnapshotRetentionLimit,
		}, names["replicationGroup"][0])
		cost.Add(scope, component.TerraformElastiCacheParameterGroup, resource.Name, map[string]any{"family": args.Family}, names["parameterGroup"][0])
		cost.Add(scope, component.TerraformElastiCacheSubnetGroup, resource.Name, map[string]any{}, names["subnetGroup"][0])
		cost.AddPlain(scope, component.TerraformSSMParameter, resource.Name, map[string]any{"type": "SecureString"})
	}
	return nil
}
