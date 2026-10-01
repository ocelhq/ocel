package queues

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/ocelhq/ocel/pkg/naming"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	fifoSuffix = ".fifo"

	maxQueueNameLen    = 80
	maxTopicNameLen    = 256
	maxGroupNameLen    = 64
	maxScheduleNameLen = 64
	consumerDigitsLen  = 8

	deadLetterSegment   = "dlq"
	scheduleGroupSuffix = "cron"
)

func QueueName(project, env, topic, consumer string, fifo bool) string {
	return fitName(maxQueueNameLen, fifo, project, env, []string{topic, consumer}, consumerDigits(topic, consumer))
}

func DeadLetterQueueName(project, env, topic, consumer string, fifo bool) string {
	return fitName(maxQueueNameLen, fifo, project, env, []string{topic, consumer}, consumerDigits(topic, consumer), deadLetterSegment)
}

func TopicName(project, env, topic string, fifo bool) string {
	return fitName(maxTopicNameLen, fifo, project, env, []string{topic})
}

func ScheduleGroupName(project, env string) string {
	return naming.Fit(maxGroupNameLen, naming.WordSeparator, naming.Fixed(awsports.AppScope), naming.Fixed(project), naming.Compressible(env), naming.Fixed(scheduleGroupSuffix))
}

func CronScheduleName(task string) string {
	return naming.Fit(maxScheduleNameLen, naming.WordSeparator, naming.Compressible(task))
}

func IsFIFO(name string) bool {
	return len(name) > len(fifoSuffix) && name[len(name)-len(fifoSuffix):] == fifoSuffix
}

func fitName(max int, fifo bool, project, env string, compressible []string, fixed ...string) string {
	if fifo {
		max -= len(fifoSuffix)
	}
	segments := []naming.Segment{naming.Fixed(awsports.AppScope), naming.Fixed(project), naming.Fixed(env)}
	for _, part := range compressible {
		segments = append(segments, naming.Compressible(part))
	}
	for _, part := range fixed {
		segments = append(segments, naming.Fixed(part))
	}
	name := naming.Fit(max, naming.WordSeparator, segments...)
	if fifo {
		return name + fifoSuffix
	}
	return name
}

func consumerDigits(topic, consumer string) string {
	sum := sha256.Sum256([]byte(topic + "/" + consumer))
	return hex.EncodeToString(sum[:])[:consumerDigitsLen]
}
