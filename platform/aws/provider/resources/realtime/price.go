package realtime

import (
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

func shape(cost component.CostTree, scope string, _ component.Stack, realtimes []provider.Resource) error {
	if len(realtimes) == 0 {
		return nil
	}
	authorizer := string(naming.KindRealtimeAuthorizer)
	cost.AddPlain(scope, component.TerraformAppSyncAPI, string(naming.KindRealtime), map[string]any{})
	cost.AddPlain(scope, component.TerraformLambdaFunction, authorizer, map[string]any{
		"runtime":           realtimeAuthorizerRuntime,
		"memory_size":       realtimeAuthorizerMemoryMB,
		"timeout":           realtimeAuthorizerTimeoutSeconds,
		"architectures":     []any{arch.ARM64},
		"ephemeral_storage": map[string]any{"size": component.LambdaDefaultEphemeralMB},
	})
	cost.AddPlain(scope, component.TerraformLogGroup, authorizer, map[string]any{"retention_in_days": component.LambdaLogRetentionDays})
	for _, resource := range realtimes {
		cost.AddPlain(scope, component.TerraformSecret, resource.Declared, map[string]any{"managed_by": "ocel"})
	}
	return nil
}
