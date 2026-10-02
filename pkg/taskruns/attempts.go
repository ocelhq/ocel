package taskruns

import "github.com/ocelhq/ocel/pkg/provider"

func AttemptsFor(policy provider.RetryPolicy, requested int32) int {
	if requested > 0 && int(requested) < policy.MaxAttempts {
		return int(requested)
	}
	return policy.MaxAttempts
}
