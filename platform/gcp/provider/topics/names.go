package topics

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	nameSeparator  = "."
	jobSeparator   = "_"
	deadLetterWord = "dead"
)

type Names struct {
	Namespace provider.Namespace
	Scope     Scope
}

func (n Names) prefix() string {
	return strings.Join([]string{
		string(n.Namespace), string(n.Scope.Tier), naming.Sanitize(n.Scope.Slug), naming.Sanitize(n.Scope.Environment),
	}, nameSeparator)
}

func (n Names) Topic(topic string) string {
	return n.prefix() + nameSeparator + topic
}

func (n Names) Subscription(topic, consumer string) string {
	return n.Topic(topic) + nameSeparator + consumer
}

func (n Names) DeadLetterTopic(topic, consumer string) string {
	return n.Subscription(topic, consumer) + nameSeparator + deadLetterWord
}

func (n Names) ScheduleJob(task string) string {
	return strings.ReplaceAll(n.Topic(task), nameSeparator, jobSeparator)
}
