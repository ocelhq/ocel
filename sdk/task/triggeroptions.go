package task

import (
	"maps"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	taskv1 "ocel.dev/internal/proto/app/task/v1"
	topicv1 "ocel.dev/internal/proto/app/topic/v1"
)

// A TriggerOption tunes one run a task's Trigger or BatchTrigger starts.
type TriggerOption interface {
	applyTrigger(*triggerSettings)
}

type triggerSettings struct {
	due               func(now time.Time) time.Time
	idempotencyKey    string
	key               string
	lane              topicv1.Lane
	ttl               *durationpb.Duration
	idempotencyKeyTTL *durationpb.Duration
	debounce          *taskv1.Debounce
	maxAttempts       int32
	tags              []string
	metadata          map[string]any
}

type triggerOption func(*triggerSettings)

func (o triggerOption) applyTrigger(s *triggerSettings) { o(s) }

// A Lane is how soon a run is read relative to others waiting: high, default
// and low lanes are read in a 6:3:1 ratio, so none waits forever. A Lane is a
// [TriggerOption], putting the run in that lane.
type Lane int32

const (
	// LaneHigh is read six times as often as LaneLow.
	LaneHigh Lane = Lane(topicv1.Lane_LANE_HIGH)
	// LaneDefault is where a run goes when no lane is given.
	LaneDefault Lane = Lane(topicv1.Lane_LANE_DEFAULT)
	// LaneLow is read once for every six reads of LaneHigh.
	LaneLow Lane = Lane(topicv1.Lane_LANE_LOW)
)

func (l Lane) applyTrigger(s *triggerSettings) { s.lane = topicv1.Lane(l) }

// Delay makes a run due this long from now, up to 30 days.
func Delay(wait time.Duration) TriggerOption {
	return triggerOption(func(s *triggerSettings) {
		s.due = func(now time.Time) time.Time { return now.Add(wait) }
	})
}

// DelayUntil makes a run due at a time, at most 30 days from now.
func DelayUntil(due time.Time) TriggerOption {
	return triggerOption(func(s *triggerSettings) {
		s.due = func(time.Time) time.Time { return due }
	})
}

// IdempotencyKey makes a repeated trigger with the same key return the run
// the first one made, rather than make another.
func IdempotencyKey(key string) TriggerOption {
	return triggerOption(func(s *triggerSettings) { s.idempotencyKey = key })
}

// IdempotencyKeyTTL is how long an [IdempotencyKey] keeps returning the
// first run. Left out, it is 30 days.
func IdempotencyKeyTTL(ttl time.Duration) TriggerOption {
	return triggerOption(func(s *triggerSettings) { s.idempotencyKeyTTL = durationpb.New(ttl) })
}

// Key is what an ordered task orders by: runs with the same key are delivered
// one at a time, in order.
func Key(key string) TriggerOption {
	return triggerOption(func(s *triggerSettings) { s.key = key })
}

// Debounce folds triggers that share key into one run, due wait after the
// last of them.
func Debounce(key string, wait time.Duration) TriggerOption {
	return triggerOption(func(s *triggerSettings) {
		s.debounce = &taskv1.Debounce{Key: key, Delay: durationpb.New(wait)}
	})
}

// MaxAttempts lowers how many attempts this run gets below the task's
// [Retry]. It never raises it.
func MaxAttempts(attempts int) TriggerOption {
	return triggerOption(func(s *triggerSettings) { s.maxAttempts = int32(attempts) })
}

// Tags labels a run, so [ocel.dev/runs.List] can find it by them.
func Tags(tags ...string) TriggerOption {
	return triggerOption(func(s *triggerSettings) { s.tags = tags })
}

// RunMetadata is kept with a run and returned on its [ocel.dev/runs.Run]
// record. Its values are anything encoding/json writes as JSON.
func RunMetadata(metadata map[string]any) TriggerOption {
	return triggerOption(func(s *triggerSettings) { s.metadata = maps.Clone(metadata) })
}
