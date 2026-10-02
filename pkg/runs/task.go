package runs

import "github.com/ocelhq/ocel/pkg/provider"

func IsTask(topic *provider.TopicSpec) bool {
	return len(topic.Consumers) == 1 && topic.Consumers[0].Exclusive
}

func ExecutionOf(messageID, consumer string) string { return messageID + "-" + consumer }
