package bucket

import (
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	awscost "github.com/ocelhq/ocel/platform/aws/provider/cost"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

func inventory(cost component.CostTree, scope string, at component.Stack, declared []provider.Resource) error {
	for _, resource := range declared {
		names, err := NamesFor(at, resource.Name)
		if err != nil {
			return err
		}
		cost.Add(scope, awscost.TerraformS3Bucket, resource.Name, map[string]any{}, names.bucket())
		cost.Add(scope, awscost.TerraformLambdaFunction, resource.Name+"-"+uploadCompleterSuffix, map[string]any{
			"runtime":           uploadCompleterRuntime,
			"memory_size":       awsports.LambdaDefaultMemoryMB,
			"timeout":           uploadCompleterTimeoutSeconds,
			"architectures":     []any{arch.X8664},
			"ephemeral_storage": map[string]any{"size": awsports.LambdaDefaultEphemeralMB},
		}, names.completer())
		cost.Add(scope, awscost.TerraformLogGroup, resource.Name+"-"+uploadCompleterSuffix, map[string]any{"retention_in_days": component.LambdaLogRetentionDays}, names.completerLogGroup())
	}
	return nil
}
