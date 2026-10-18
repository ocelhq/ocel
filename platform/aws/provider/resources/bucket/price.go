package bucket

import (
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

func shape(cost component.CostTree, scope string, at component.Stack, declared []provider.Resource) error {
	for _, resource := range declared {
		names := resourceNames(at.Project, at.Env, resource.Name)
		cost.Add(scope, component.TerraformS3Bucket, resource.Name, map[string]any{}, names["bucket"])
		cost.Add(scope, component.TerraformLambdaFunction, resource.Name+"-"+uploadCompleterLocalName, map[string]any{
			"runtime":           uploadCompleterRuntime,
			"memory_size":       component.LambdaDefaultMemoryMB,
			"timeout":           uploadCompleterTimeoutSeconds,
			"architectures":     []any{arch.X8664},
			"ephemeral_storage": map[string]any{"size": component.LambdaDefaultEphemeralMB},
		}, names["uploadCompleter"])
		cost.Add(scope, component.TerraformLogGroup, resource.Name+"-"+uploadCompleterLocalName, map[string]any{"retention_in_days": component.LambdaLogRetentionDays}, names["uploadCompleterLogGroup"])
	}
	return nil
}
