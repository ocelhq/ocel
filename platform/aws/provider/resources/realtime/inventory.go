package realtime

import (
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

func inventory(tree component.InventoryTree, scope string, _ component.Stack, realtimes []provider.Resource) error {
	if len(realtimes) == 0 {
		return nil
	}
	authorizer := string(naming.KindRealtimeAuthorizer)
	tree.AddPlain(scope, component.TerraformAppSyncAPI, string(naming.KindRealtime), map[string]any{})
	tree.AddPlain(scope, component.TerraformLambdaFunction, authorizer, map[string]any{
		"runtime":           realtimeAuthorizerRuntime,
		"memory_size":       realtimeAuthorizerMemoryMB,
		"timeout":           realtimeAuthorizerTimeoutSeconds,
		"architectures":     []any{arch.ARM64},
		"ephemeral_storage": map[string]any{"size": component.LambdaDefaultEphemeralMB},
	})
	tree.AddPlain(scope, component.TerraformLogGroup, authorizer, map[string]any{"retention_in_days": component.LambdaLogRetentionDays})
	for _, resource := range realtimes {
		tree.AddPlain(scope, component.TerraformSecret, resource.Declared, map[string]any{})
	}
	return nil
}
