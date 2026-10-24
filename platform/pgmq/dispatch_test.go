package pgmq

import (
	"slices"
	"testing"
	"time"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func aTask(mods ...func(*contractv1.ManifestTopic)) *contractv1.ManifestTopic {
	topic := &contractv1.ManifestTopic{Consumers: []*contractv1.ManifestConsumer{{Worker: "worker", Exclusive: true}}}
	for _, mod := range mods {
		mod(topic)
	}
	return topic
}

func TestAnIdleQueuePollsHalfAsOftenAfterEachEmptyReadDownToOnceEveryTwoSeconds(t *testing.T) {
	t.Parallel()

	interval := pollInterval
	var got []time.Duration
	for range 6 {
		interval = lengthenPollInterval(interval)
		got = append(got, interval)
	}
	want := []time.Duration{400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond, 2 * time.Second, 2 * time.Second, 2 * time.Second}
	if !slices.Equal(got, want) {
		t.Errorf("poll intervals after each empty read = %v, want %v", got, want)
	}
}
