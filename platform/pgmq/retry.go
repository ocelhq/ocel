package pgmq

import (
	"math/rand/v2"
	"time"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type retryPolicy struct {
	maxAttempts        int
	minDelay, maxDelay time.Duration
}

func retryPolicyOf(deployed deployedConsumer) retryPolicy {
	policy := retryPolicy{maxAttempts: provider.DefaultRetryMaxAttempts, minDelay: provider.DefaultRetryMinDelay, maxDelay: provider.DefaultRetryMaxDelay}
	for _, declared := range []*resourcesv1.RetryPolicy{deployed.topic.GetRetry(), deployed.consumer.GetRetry()} {
		if declared == nil {
			continue
		}
		if declared.GetMaxAttempts() > 0 {
			policy.maxAttempts = int(declared.GetMaxAttempts())
		}
		if declared.GetMinDelay() != nil {
			policy.minDelay = declared.GetMinDelay().AsDuration()
		}
		if declared.GetMaxDelay() != nil {
			policy.maxDelay = declared.GetMaxDelay().AsDuration()
		}
	}
	policy.maxDelay = max(policy.maxDelay, policy.minDelay)
	return policy
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
