package topics

import (
	"time"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type retryPolicy struct {
	maxAttempts        int
	minDelay, maxDelay time.Duration
}

func retryPolicyOf(topic *contractv1.ManifestTopic, consumer *contractv1.ManifestConsumer) retryPolicy {
	policy := retryPolicy{maxAttempts: provider.DefaultRetryMaxAttempts, minDelay: provider.DefaultRetryMinDelay, maxDelay: provider.DefaultRetryMaxDelay}
	for _, declared := range []*resourcesv1.RetryPolicy{topic.GetRetry(), consumer.GetRetry()} {
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

func isTask(topic *contractv1.ManifestTopic) bool {
	return len(topic.GetConsumers()) == 1 && topic.GetConsumers()[0].GetExclusive()
}
