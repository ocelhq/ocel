package pgmq

import (
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/topiclimits"
)

func retryPolicyOf(deployed deployedConsumer) provider.RetryPolicy {
	return topiclimits.ResolveRetryPolicy(deployed.topic.GetRetry(), deployed.consumer.GetRetry())
}
