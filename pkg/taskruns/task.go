package taskruns

import (
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func IsTask(topic *contractv1.ManifestTopic) bool {
	return len(topic.GetConsumers()) == 1 && topic.GetConsumers()[0].GetExclusive()
}

func ExecutionOf(messageID, consumer string) string { return messageID + "-" + consumer }

func LaneName(lane topicv1.Lane) string {
	switch lane {
	case topicv1.Lane_LANE_HIGH:
		return "high"
	case topicv1.Lane_LANE_LOW:
		return "low"
	default:
		return "default"
	}
}
