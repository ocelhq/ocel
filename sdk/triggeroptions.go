package ocel

import (
	"maps"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	taskv1 "ocel.dev/internal/proto/app/task/v1"
	topicv1 "ocel.dev/internal/proto/app/topic/v1"
)

// A TriggerOption tunes one run a task's Trigger or BatchTrigger starts.
type TriggerOption interface {
	applyTrigger(*triggerSettings)
}

// A SendOption tunes one message a topic's Send publishes.
type SendOption interface {
	applySend(*sendSettings)
}

// A TriggerSendOption tunes a run or a message alike.
type TriggerSendOption interface {
	TriggerOption
	SendOption
}

// A DelayOption is when a run or message is due: an option to Trigger and
// Send, and the new due time [RescheduleRun] takes.
type DelayOption interface {
	TriggerSendOption
	computeDueAt(now time.Time) time.Time
}

// A TriggerRunListOption tunes a trigger, or narrows a [ListRuns] alike.
type TriggerRunListOption interface {
	TriggerOption
	RunListOption
}

type sendSettings struct {
	due            DelayOption
	idempotencyKey string
	key            string
	lane           topicv1.Lane
}

type triggerSettings struct {
	sendSettings
	ttl               *durationpb.Duration
	idempotencyKeyTTL *durationpb.Duration
	debounce          *taskv1.Debounce
	maxAttempts       int32
	tags              []string
	metadata          map[string]any
}

func (s sendSettings) encodeDueAt() *timestamppb.Timestamp {
	if s.due == nil {
		return nil
	}
	return timestamppb.New(s.due.computeDueAt(time.Now()))
}

type delayOption time.Duration

func (o delayOption) applySend(s *sendSettings)            { s.due = o }
func (o delayOption) applyTrigger(s *triggerSettings)      { s.due = o }
func (o delayOption) computeDueAt(now time.Time) time.Time { return now.Add(time.Duration(o)) }

// Delay makes a run or message due this long from now, up to 30 days.
func Delay(wait time.Duration) DelayOption { return delayOption(wait) }

type delayUntilOption time.Time

func (o delayUntilOption) applySend(s *sendSettings)        { s.due = o }
func (o delayUntilOption) applyTrigger(s *triggerSettings)  { s.due = o }
func (o delayUntilOption) computeDueAt(time.Time) time.Time { return time.Time(o) }

// DelayUntil makes a run or message due at a time, at most 30 days from now.
func DelayUntil(due time.Time) DelayOption { return delayUntilOption(due) }

// IdempotencyKey makes a repeated trigger or send with the same key return
// the run or message the first one made, rather than make another.
func IdempotencyKey(key string) TriggerSendOption {
	return option{
		trigger: func(s *triggerSettings) { s.idempotencyKey = key },
		send:    func(s *sendSettings) { s.idempotencyKey = key },
	}
}

// IdempotencyKeyTTL is how long an [IdempotencyKey] keeps returning the
// first run. Left out, it is 30 days.
func IdempotencyKeyTTL(ttl time.Duration) TriggerOption {
	return option{trigger: func(s *triggerSettings) { s.idempotencyKeyTTL = durationpb.New(ttl) }}
}

// Key is what an ordered task or topic orders by: runs or messages with the
// same key are delivered one at a time, in order.
func Key(key string) TriggerSendOption {
	return option{
		trigger: func(s *triggerSettings) { s.key = key },
		send:    func(s *sendSettings) { s.key = key },
	}
}

// Debounce folds triggers that share key into one run, due wait after the
// last of them.
func Debounce(key string, wait time.Duration) TriggerOption {
	return option{trigger: func(s *triggerSettings) {
		s.debounce = &taskv1.Debounce{Key: key, Delay: durationpb.New(wait)}
	}}
}

// MaxAttempts lowers how many attempts this run gets below the task's
// [RetryPolicy]. It never raises it.
func MaxAttempts(attempts int) TriggerOption {
	return option{trigger: func(s *triggerSettings) { s.maxAttempts = int32(attempts) }}
}

// Tags labels a run, so [ListRuns] can find it by them. Given to
// [ListRuns], it lists only the runs carrying every tag.
func Tags(tags ...string) TriggerRunListOption {
	return option{
		trigger: func(s *triggerSettings) { s.tags = tags },
		runList: func(s *runListSettings) { s.tags = tags },
	}
}

// RunMetadata is kept with a run and returned on its [Run] record. Its
// values are anything encoding/json writes as JSON.
func RunMetadata(metadata map[string]any) TriggerOption {
	return option{trigger: func(s *triggerSettings) { s.metadata = maps.Clone(metadata) }}
}
