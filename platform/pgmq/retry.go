package pgmq

import (
	"math/rand/v2"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
)

func retryPolicyOf(deployed deployedConsumer) provider.RetryPolicy {
	return provider.ResolveRetryPolicy(deployed.topic.GetRetry(), deployed.consumer.GetRetry())
}

func attemptsFor(policy provider.RetryPolicy, requested int32) int {
	if requested > 0 && int(requested) < policy.MaxAttempts {
		return int(requested)
	}
	return policy.MaxAttempts
}

func backoff(policy provider.RetryPolicy, attempt int, random float64) time.Duration {
	delay := policy.MinDelay
	for i := 1; i < attempt && delay < policy.MaxDelay; i++ {
		delay *= 2
	}
	delay = min(delay, policy.MaxDelay)
	return delay/2 + time.Duration(random*float64(delay/2))
}

func jitter() float64 { return rand.Float64() }
