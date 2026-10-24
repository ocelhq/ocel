package pgmq

import (
	"testing"

	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
)

func TestLanesShareReadsSixToThreeToOne(t *testing.T) {
	t.Parallel()

	var turns laneTurns
	if got := turns.share(allLanes, 10); got[topicv1.Lane_LANE_HIGH] != 6 || got[topicv1.Lane_LANE_DEFAULT] != 3 || got[topicv1.Lane_LANE_LOW] != 1 {
		t.Errorf("one read of 10 = %v, want 6 high, 3 default, 1 low", got)
	}

	var oneAtATime laneTurns
	counts := map[topicv1.Lane]int{}
	for range 20 {
		for lane, n := range oneAtATime.share(allLanes, 1) {
			counts[lane] += n
		}
	}
	if counts[topicv1.Lane_LANE_HIGH] != 12 || counts[topicv1.Lane_LANE_DEFAULT] != 6 || counts[topicv1.Lane_LANE_LOW] != 2 {
		t.Errorf("20 reads of 1 = %v, want 12 high, 6 default, 2 low", counts)
	}
}

func TestAConsumerThatNamesItsLanesWeighsOnlyThose(t *testing.T) {
	t.Parallel()

	var turns laneTurns
	got := turns.share([]topicv1.Lane{topicv1.Lane_LANE_HIGH, topicv1.Lane_LANE_LOW}, 7)
	if got[topicv1.Lane_LANE_HIGH] != 6 || got[topicv1.Lane_LANE_LOW] != 1 || got[topicv1.Lane_LANE_DEFAULT] != 0 {
		t.Errorf("one read of 7 over high and low = %v, want 6 high and 1 low", got)
	}
}
