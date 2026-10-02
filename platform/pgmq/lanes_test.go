package pgmq

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/taskruns"
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

func TestWaitingMessagesAreDeliveredFromEachLaneByWeightWithoutStarvingLow(t *testing.T) {
	worker := newWorker(t, func(*topicv1.Envelope) reply { return reply{status: http.StatusOK, hold: 10 * time.Millisecond} })
	engine := applied(t, map[string]*contractv1.ManifestTopic{
		"orders": aTopic(&contractv1.ManifestConsumer{Name: "email", Worker: "worker", Concurrency: 1}),
	}, map[string]Worker{"worker": {URL: worker.server.URL}})
	for _, lane := range []topicv1.Lane{topicv1.Lane_LANE_LOW, topicv1.Lane_LANE_DEFAULT, topicv1.Lane_LANE_HIGH} {
		for n := range 10 {
			send(t, engine, "orders", fmt.Sprintf(`{"lane":%q,"n":%d}`, taskruns.LaneName(lane), n), &topicv1.SendRequest{Lane: lane})
		}
	}
	startDispatch(t, engine)

	got := awaitDelivered(t, worker, 10)
	counts := map[string]int{}
	for _, d := range got[:10] {
		lane, _ := fieldOf(d.envelope.GetPayload(), "lane").(string)
		counts[lane]++
	}
	if counts["high"] != 6 || counts["default"] != 3 || counts["low"] != 1 {
		t.Errorf("the first 10 deliveries by lane = %v, want 6 high, 3 default, 1 low", counts)
	}
}
