package topic

import (
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"ocel.dev/internal/delivery"
	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
	topicv1 "ocel.dev/internal/proto/app/topic/v1"
	"ocel.dev/worker"
)

// An Option tunes the topic [New] declares.
type Option interface {
	applyTopic(*settings)
}

// A ConsumerOption tunes a consumer a topic's Consumer or BatchConsumer
// declares.
type ConsumerOption interface {
	applyConsumer(*consumerSettings)
}

// A SendOption tunes one message a topic's Send publishes.
type SendOption interface {
	applySend(*sendSettings)
}

type settings struct {
	config *resourcesv1.TopicConfig
}

type consumerSettings struct {
	config *resourcesv1.ConsumerConfig
}

type sendSettings struct {
	due            func(now time.Time) time.Time
	idempotencyKey string
	key            string
	lane           topicv1.Lane
}

type option func(*settings)

func (o option) applyTopic(s *settings) { o(s) }

type consumerOption func(*consumerSettings)

func (o consumerOption) applyConsumer(s *consumerSettings) { o(s) }

type sendOption func(*sendSettings)

func (o sendOption) applySend(s *sendSettings) { o(s) }

// A Retry is how often, and how far apart, a failed attempt at a message is
// tried again. Given to [New] it applies to every consumer of the topic; given
// to Consumer or BatchConsumer, to that consumer alone. A field left zero is
// left to the default: 3 attempts, 1s after the first failure, backing off to
// at most 60s.
type Retry struct {
	// MaxAttempts is how many attempts a message gets in all, 1 to 100.
	MaxAttempts int
	// MinDelay is how long the first retry waits.
	MinDelay time.Duration
	// MaxDelay is the longest any retry waits, at most 600s.
	MaxDelay time.Duration
}

func (r Retry) applyTopic(s *settings) {
	s.config.Retry = delivery.EncodeRetry(r.MaxAttempts, r.MinDelay, r.MaxDelay)
}

func (r Retry) applyConsumer(s *consumerSettings) {
	s.config.Retry = delivery.EncodeRetry(r.MaxAttempts, r.MinDelay, r.MaxDelay)
}

// Schema is the JSON Schema document a payload is checked against, written
// out as JSON. Ocel derives none from a Go type, so a topic without one takes
// any payload its type decodes from.
func Schema(jsonSchema string) Option {
	return option(func(s *settings) { s.config.Schema = jsonSchema })
}

// Ordered delivers the messages that share a [Key] one at a time, in the
// order they were sent, and holds the key back while one of them fails.
func Ordered() Option {
	return option(func(s *settings) { s.config.Ordered = true })
}

// Concurrency is how many messages the consumer handles at once, at most, 1
// to 1000.
func Concurrency(limit int) ConsumerOption {
	return consumerOption(func(s *consumerSettings) { s.config.Concurrency = int32(limit) })
}

// MaxDuration is how long one attempt may run. An attempt that runs longer is
// a failed attempt, and is retried.
func MaxDuration(limit time.Duration) ConsumerOption {
	return consumerOption(func(s *consumerSettings) { s.config.MaxDuration = durationpb.New(limit) })
}

// BatchTimeout is how long a batch consumer waits to fill a batch before
// taking what has arrived, up to 300s.
func BatchTimeout(wait time.Duration) ConsumerOption {
	return consumerOption(func(s *consumerSettings) {
		delivery.EnsureBatchPolicy(&s.config.Batch).Timeout = durationpb.New(wait)
	})
}

// UseWorker runs the consumer on w rather than on the default worker, named
// "worker".
func UseWorker(w *worker.Worker) ConsumerOption {
	return consumerOption(func(s *consumerSettings) { s.config.Worker = w.Name() })
}

// A Lane is how soon a message is read relative to others waiting: high,
// default and low lanes are read in a 6:3:1 ratio, so none waits forever. A
// Lane is also a [SendOption], putting the message in that lane.
type Lane int32

const (
	// LaneHigh is read six times as often as LaneLow.
	LaneHigh Lane = Lane(topicv1.Lane_LANE_HIGH)
	// LaneDefault is where a message goes when no lane is given.
	LaneDefault Lane = Lane(topicv1.Lane_LANE_DEFAULT)
	// LaneLow is read once for every six reads of LaneHigh.
	LaneLow Lane = Lane(topicv1.Lane_LANE_LOW)
)

func (l Lane) applySend(s *sendSettings) { s.lane = topicv1.Lane(l) }

// Lanes is the lanes a consumer reads from, and their weighting is the
// ratio [Lane] describes. A consumer of an ordered topic reads in order
// instead, and takes no lanes.
func Lanes(lanes ...Lane) ConsumerOption {
	return consumerOption(func(s *consumerSettings) {
		s.config.Lanes = nil
		for _, lane := range lanes {
			s.config.Lanes = append(s.config.Lanes, topicv1.Lane(lane))
		}
	})
}

// Delay makes a message due this long from now, up to 30 days.
func Delay(wait time.Duration) SendOption {
	return sendOption(func(s *sendSettings) {
		s.due = func(now time.Time) time.Time { return now.Add(wait) }
	})
}

// DelayUntil makes a message due at a time, at most 30 days from now.
func DelayUntil(due time.Time) SendOption {
	return sendOption(func(s *sendSettings) {
		s.due = func(time.Time) time.Time { return due }
	})
}

// IdempotencyKey makes a repeated send with the same key return the message
// the first one made, rather than make another.
func IdempotencyKey(key string) SendOption {
	return sendOption(func(s *sendSettings) { s.idempotencyKey = key })
}

// Key is what an ordered topic orders by: messages with the same key are
// delivered one at a time, in order.
func Key(key string) SendOption {
	return sendOption(func(s *sendSettings) { s.key = key })
}
