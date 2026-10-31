package kv

import (
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	awscost "github.com/ocelhq/ocel/platform/aws/provider/cost"
)

func inventory(cost component.CostTree, scope string, at component.Stack, declared []provider.Resource) error {
	for _, resource := range declared {
		args, err := translateKV(resource.Name, resource.KV)
		if err != nil {
			return err
		}
		names, err := NamesFor(at, resource.Name, args.Family)
		if err != nil {
			return err
		}
		cost.Add(scope, awscost.TerraformElastiCacheReplicationGroup, resource.Name, map[string]any{
			"engine":                     args.Engine,
			"engine_version":             args.EngineVersion,
			"node_type":                  args.NodeType,
			"num_cache_clusters":         args.NumCacheClusters,
			"automatic_failover_enabled": args.AutomaticFailoverEnabled,
			"multi_az_enabled":           args.MultiAZEnabled,
			"snapshot_retention_limit":   args.SnapshotRetentionLimit,
		}, names.replicationGroup())
		cost.Add(scope, awscost.TerraformElastiCacheParameterGroup, resource.Name, map[string]any{"family": args.Family}, names.parameterGroup())
		cost.Add(scope, awscost.TerraformElastiCacheSubnetGroup, resource.Name, map[string]any{}, names.subnetGroup())
		cost.AddPlain(scope, awscost.TerraformSSMParameter, resource.Name, map[string]any{"type": "SecureString"})
	}
	return nil
}
