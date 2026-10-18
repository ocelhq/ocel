package topics

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/runs"
)

const delayQueue = "projects/acme/locations/europe-west1/queues/ocel-production-delays"

func TestTheDelayTasksOfMessagesSentInOrderAreNotNamedInThatOrder(t *testing.T) {
	delays := Delays{Queue: delayQueue}
	start := time.Now()
	var names []string
	for i := range 20 {
		names = append(names, delays.taskOf(runs.Message{ID: runs.NewMessageID(start), Delivery: runs.NewMessageID(start.Add(time.Duration(i) * time.Millisecond))}))
	}
	if slices.IsSorted(names) {
		t.Errorf("delay tasks named %v sort in the order they were sent, which hot-spots one range of the shared queue", names)
	}
}

func TestADelayTaskIsNamedForTheDeliveryItCarries(t *testing.T) {
	delays := Delays{Queue: delayQueue}
	first := runs.Message{ID: "01K0000000000000000000000A", Delivery: "01K0000000000000000000000D"}
	if again := first; delays.taskOf(first) != delays.taskOf(again) {
		t.Error("sending one delivery twice names two delay tasks, so a retried send would publish its message twice")
	}
	rescheduled := first
	rescheduled.Delivery = "01K0000000000000000000000E"
	if delays.taskOf(first) == delays.taskOf(rescheduled) {
		t.Error("a rescheduled run's new delivery takes its old delay task's name, which Cloud Tasks refuses for an hour after the old one ran")
	}
	if name := delays.taskOf(first); !strings.HasPrefix(name, delayQueue+"/tasks/") || !strings.HasSuffix(name, "-"+first.Delivery) {
		t.Errorf("the delay task is named %s, want a hashed prefix and the delivery under %s", name, delayQueue)
	}
}

func TestADelayedPublishToEveryConsumerIsNamedForItsMessage(t *testing.T) {
	delays := Delays{Queue: delayQueue}
	published := runs.Message{ID: "01K0000000000000000000000A"}
	if name := delays.taskOf(published); !strings.HasSuffix(name, "-"+published.ID) {
		t.Errorf("a delayed publish with no delivery is named %s, want it named for its message %s", name, published.ID)
	}
}
