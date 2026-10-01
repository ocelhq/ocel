package provider

import (
	"time"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

const (
	DefaultRetryMaxAttempts = 3
	DefaultRetryMinDelay    = time.Second
	DefaultRetryMaxDelay    = time.Minute
)

type RetryPolicy struct {
	MaxAttempts int
	MinDelay    time.Duration
	MaxDelay    time.Duration
}

func ResolveRetryPolicy(declared ...*resourcesv1.RetryPolicy) RetryPolicy {
	policy := RetryPolicy{MaxAttempts: DefaultRetryMaxAttempts, MinDelay: DefaultRetryMinDelay, MaxDelay: DefaultRetryMaxDelay}
	for _, retry := range declared {
		if retry.GetMaxAttempts() > 0 {
			policy.MaxAttempts = int(retry.GetMaxAttempts())
		}
		if retry.GetMinDelay() != nil {
			policy.MinDelay = retry.GetMinDelay().AsDuration()
		}
		if retry.GetMaxDelay() != nil {
			policy.MaxDelay = retry.GetMaxDelay().AsDuration()
		}
	}
	policy.MaxDelay = max(policy.MaxDelay, policy.MinDelay)
	return policy
}
