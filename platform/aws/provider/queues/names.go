package queues

import (
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	fifoSuffix = ".fifo"

	maxQueueNameLen         = 80
	maxTopicNameLen         = 256
	maxScheduleGroupNameLen = 64
	maxScheduleNameLen      = 64

	deadLetterSuffix  = "dlq"
	scheduleGroupWord = "cron"
)

type Names struct {
	Namespace string
	Project   string
	Env       string
}

type QueueNames struct {
	Queue      string
	DeadLetter string
}

func (n Names) Queues(topic, consumer string, fifo bool) (QueueNames, error) {
	limit := maxQueueNameLen
	if fifo {
		limit -= len(fifoSuffix)
	}
	name := naming.PhysicalName{
		Fence:    naming.AppFence(n.Namespace),
		Project:  n.Project,
		Env:      n.Env,
		Declared: naming.Join(naming.WordSeparator, topic, consumer),
		Tag:      naming.HashTag(n.Namespace, n.Project, n.Env, "", string(naming.KindTopic), topic, consumer),
	}
	queue, err := name.Fit(limit, deadLetterSuffix)
	if err != nil {
		return QueueNames{}, refused("topic "+topic+"'s consumer "+consumer+"'s queue", limit, err)
	}
	deadLetter := queue + naming.WordSeparator + deadLetterSuffix
	if fifo {
		return QueueNames{Queue: queue + fifoSuffix, DeadLetter: deadLetter + fifoSuffix}, nil
	}
	return QueueNames{Queue: queue, DeadLetter: deadLetter}, nil
}

func (n Names) Topic(topic string, fifo bool) (string, error) {
	limit := maxTopicNameLen
	if fifo {
		limit -= len(fifoSuffix)
	}
	at := naming.Coordinate{Project: n.Project, Env: n.Env, App: naming.InfraApp, Kind: naming.KindTopic, Name: topic}
	name, err := at.AppResourceName(n.Namespace, at.HashTag(n.Namespace)).Fit(limit)
	if err != nil {
		return "", refused("topic "+topic, limit, err)
	}
	if fifo {
		return name + fifoSuffix, nil
	}
	return name, nil
}

func (n Names) ScheduleGroup() (string, error) {
	at := naming.Coordinate{Project: n.Project, Env: n.Env, App: naming.InfraApp, Kind: naming.KindWorker, Name: scheduleGroupWord}
	name, err := at.AppResourceName(n.Namespace, at.HashTag(n.Namespace)).Fit(maxScheduleGroupNameLen)
	if err != nil {
		return "", refused("cron schedule group", maxScheduleGroupNameLen, err)
	}
	return name, nil
}

func (n Names) Schedules(task string, expressions int) ([]string, error) {
	at := naming.Coordinate{Project: n.Project, Env: n.Env, App: naming.InfraApp, Kind: naming.KindTopic, Name: task}
	physical := at.AppResourceName(n.Namespace, at.HashTag(n.Namespace))
	if expressions <= 1 {
		name, err := physical.Fit(maxScheduleNameLen)
		if err != nil {
			return nil, refused("task "+task+"'s schedule", maxScheduleNameLen, err)
		}
		return []string{name}, nil
	}
	name, err := physical.Fit(maxScheduleNameLen, strconv.Itoa(expressions))
	if err != nil {
		return nil, refused("task "+task+"'s schedules", maxScheduleNameLen, err)
	}
	names := make([]string, 0, expressions)
	for i := range expressions {
		names = append(names, name+naming.WordSeparator+strconv.Itoa(i+1))
	}
	return names, nil
}

func IsFIFO(name string) bool {
	return len(name) > len(fifoSuffix) && strings.HasSuffix(name, fifoSuffix)
}

func refused(what string, limit int, err error) error {
	return refusal.Refuse(refusal.CodeInvalid, "the %s takes no AWS name within %d characters: %v", what, limit, err)
}
