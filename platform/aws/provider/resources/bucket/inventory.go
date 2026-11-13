package bucket

import (
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

func inventory(tree component.InventoryTree, scope string, at component.Stack, declared []provider.Resource) error {
	for _, resource := range declared {
		names, err := NamesFor(at, resource.Name)
		if err != nil {
			return err
		}
		tree.Add(scope, component.TerraformS3Bucket, resource.Name, map[string]any{}, names.bucket())
		tree.Add(scope, component.TerraformLambdaFunction, resource.Name+"-"+uploadCompleterSuffix, map[string]any{
			"runtime":           uploadCompleterRuntime,
			"memory_size":       component.LambdaDefaultMemoryMB,
			"timeout":           uploadCompleterTimeoutSeconds,
			"architectures":     []any{arch.X8664},
			"ephemeral_storage": map[string]any{"size": component.LambdaDefaultEphemeralMB},
		}, names.completer())
		tree.Add(scope, component.TerraformLogGroup, resource.Name+"-"+uploadCompleterSuffix, map[string]any{"retention_in_days": component.LambdaLogRetentionDays}, names.completerLogGroup())
	}
	return nil
}
