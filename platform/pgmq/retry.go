package pgmq

import (
	"math/rand/v2"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
)

type retryPolicy struct {
	maxAttempts        int
	minDelay, maxDelay time.Duration
}

func retryPolicyOf(deployed deployedConsumer) retryPolicy {
	resolved := provider.ResolveRetryPolicy(deployed.topic.GetRetry(), deployed.consumer.GetRetry())
	return retryPolicy{maxAttempts: resolved.MaxAttempts, minDelay: resolved.MinDelay, maxDelay: resolved.MaxDelay}
}

func (p retryPolicy) attemptsFor(requested int32) int {
	if requested > 0 && int(requested) < p.maxAttempts {
		return int(requested)
	}
	return p.maxAttempts
}

func (p retryPolicy) backoff(attempt int, random float64) time.Duration {
	delay := p.minDelay
	for i := 1; i < attempt && delay < p.maxDelay; i++ {
		delay *= 2
	}
	delay = min(delay, p.maxDelay)
	return delay/2 + time.Duration(random*float64(delay/2))
}

func jitter() float64 { return rand.Float64() }
