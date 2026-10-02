package runs

import (
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func IsTask(topic *contractv1.ManifestTopic) bool {
	return len(topic.GetConsumers()) == 1 && topic.GetConsumers()[0].GetExclusive()
}

func IsTaskSpec(topic *provider.TopicSpec) bool {
	return len(topic.Consumers) == 1 && topic.Consumers[0].Exclusive
}

func ExecutionOf(messageID, consumer string) string { return messageID + "-" + consumer }
