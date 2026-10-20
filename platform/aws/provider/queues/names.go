package queues

import (
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
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

func (n Names) fit(kind naming.Kind, name string, limit int, childSuffixes ...string) (string, error) {
	at := naming.Coordinate{Project: n.Project, Env: n.Env, App: naming.InfraApp, Kind: kind, Name: name}
	return at.AppResourceName(n.Namespace, at.HashTag(n.Namespace)).Fit(limit, childSuffixes...)
}

func (n Names) Queues(topic, consumer string, fifo bool) (QueueNames, error) {
	limit := maxQueueNameLen
	if fifo {
		limit -= len(fifoSuffix)
	}
	queue, err := n.fit(naming.KindTopic, topic+naming.FieldSeparator+consumer, limit, deadLetterSuffix)
	if err != nil {
		return QueueNames{}, err
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
	name, err := n.fit(naming.KindTopic, topic, limit)
	if err != nil {
		return "", err
	}
	if fifo {
		return name + fifoSuffix, nil
	}
	return name, nil
}

func (n Names) ScheduleGroup() (string, error) {
	return n.fit(naming.KindWorker, scheduleGroupWord, maxScheduleGroupNameLen)
}

func (n Names) Schedules(task string, expressions int) ([]string, error) {
	if expressions <= 1 {
		name, err := n.fit(naming.KindTopic, task, maxScheduleNameLen)
		if err != nil {
			return nil, err
		}
		return []string{name}, nil
	}
	name, err := n.fit(naming.KindTopic, task, maxScheduleNameLen, strconv.Itoa(expressions))
	if err != nil {
		return nil, err
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
