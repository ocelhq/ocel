package pgmq

import "github.com/ocelhq/ocel/pkg/provider"

func retryPolicyOf(deployed deployedConsumer) provider.RetryPolicy {
	return provider.ResolveRetryPolicy(deployed.topic.GetRetry(), deployed.consumer.GetRetry())
}
