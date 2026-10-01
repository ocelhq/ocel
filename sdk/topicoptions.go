package ocel

import (
	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
	topicv1 "ocel.dev/internal/proto/app/topic/v1"
)

// A TopicOption tunes the topic [Topic] declares.
type TopicOption interface {
	applyTopic(*topicSettings)
}

// A ConsumerOption tunes a consumer a topic's Consumer or BatchConsumer
// declares.
type ConsumerOption interface {
	applyConsumer(*consumerSettings)
}

type topicSettings struct {
	config *resourcesv1.TopicConfig
}

type consumerSettings struct {
	config *resourcesv1.ConsumerConfig
}

// A Lane is how soon a run or message is read relative to others waiting:
// high, default and low lanes are read in a 6:3:1 ratio, so none waits
// forever. A Lane is also an option to Trigger and Send, putting the run or
// message in that lane.
type Lane int32

const (
	// LaneHigh is read six times as often as LaneLow.
	LaneHigh Lane = Lane(topicv1.Lane_LANE_HIGH)
	// LaneDefault is where a run or message goes when no lane is given.
	LaneDefault Lane = Lane(topicv1.Lane_LANE_DEFAULT)
	// LaneLow is read once for every six reads of LaneHigh.
	LaneLow Lane = Lane(topicv1.Lane_LANE_LOW)
)

func (l Lane) applySend(s *sendSettings)       { s.lane = topicv1.Lane(l) }
func (l Lane) applyTrigger(s *triggerSettings) { s.lane = topicv1.Lane(l) }

// Lanes is the lanes a consumer reads from, and their weighting is the
// ratio [Lane] describes. A consumer of an ordered topic reads in order
// instead, and takes no lanes.
func Lanes(lanes ...Lane) ConsumerOption {
	return option{consumer: func(s *consumerSettings) {
		s.config.Lanes = nil
		for _, lane := range lanes {
			s.config.Lanes = append(s.config.Lanes, topicv1.Lane(lane))
		}
	}}
}
