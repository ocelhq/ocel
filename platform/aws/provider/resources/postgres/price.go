package postgres

import (
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

func shape(cost component.CostTree, scope string, at component.Stack, declared []provider.Resource) error {
	for _, resource := range declared {
		args := translate(resource.Postgres, at.Tier())
		names, err := NamesFor(at, resource.Name)
		if err != nil {
			return err
		}
		cost.Add(scope, component.TerraformRDSCluster, resource.Name, map[string]any{
			"engine":         args.Engine,
			"engine_mode":    args.EngineMode,
			"engine_version": args.EngineVersion,
			"serverlessv2_scaling_configuration": map[string]any{
				"min_capacity": args.MinCapacity,
				"max_capacity": args.MaxCapacity,
			},
			"manage_master_user_password": args.ManageMasterPassword,
		}, names.cluster())
		cost.Add(scope, component.TerraformRDSClusterInstance, resource.Name, map[string]any{
			"engine":         args.Engine,
			"instance_class": args.InstanceClass,
			"serverlessv2_scaling_configuration": map[string]any{
				"min_capacity": args.MinCapacity,
				"max_capacity": args.MaxCapacity,
			},
		}, names.instance())
		cost.AddPlain(scope, component.TerraformSecret, resource.Name, map[string]any{"managed_by": "rds"})
	}
	return nil
}
