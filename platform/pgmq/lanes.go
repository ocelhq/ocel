package pgmq

import (
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

var laneWeights = map[topicv1.Lane]int{
	topicv1.Lane_LANE_HIGH:    6,
	topicv1.Lane_LANE_DEFAULT: 3,
	topicv1.Lane_LANE_LOW:     1,
}

var allLanes = []topicv1.Lane{topicv1.Lane_LANE_HIGH, topicv1.Lane_LANE_DEFAULT, topicv1.Lane_LANE_LOW}

type laneTurns struct {
	current map[topicv1.Lane]int
}

func lanesOf(consumer *contractv1.ManifestConsumer) []topicv1.Lane {
	if lanes := consumer.GetLanes(); len(lanes) > 0 {
		return lanes
	}
	return allLanes
}

func (l *laneTurns) next(lanes []topicv1.Lane) topicv1.Lane {
	if l.current == nil {
		l.current = map[topicv1.Lane]int{}
	}
	total := 0
	var chosen topicv1.Lane
	for _, lane := range lanes {
		l.current[lane] += laneWeights[lane]
		total += laneWeights[lane]
		if chosen == topicv1.Lane_LANE_UNSPECIFIED || l.current[lane] > l.current[chosen] {
			chosen = lane
		}
	}
	l.current[chosen] -= total
	return chosen
}

func (l *laneTurns) share(lanes []topicv1.Lane, n int) map[topicv1.Lane]int {
	shares := map[topicv1.Lane]int{}
	for range n {
		shares[l.next(lanes)]++
	}
	return shares
}
