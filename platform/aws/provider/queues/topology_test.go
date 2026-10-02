package queues

import (
	"reflect"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestATopologyKeepsEachTopicsDeclarationThroughItsFile(t *testing.T) {
	t.Parallel()

	declared := &provider.TopicSpec{Ordered: true, TTL: time.Hour, Consumers: []provider.ConsumerSpec{{Name: "resize", Worker: "media", Exclusive: true, Retry: provider.RetryPolicy{MaxAttempts: 4, MinDelay: time.Second, MaxDelay: time.Minute}, Lanes: []provider.Lane{provider.LaneHigh}}}}
	rendered, err := Render(Topology{Table: "state", KeyPrefix: "PROJECT#shop#", Topics: map[string]Topic{"resize": {Declared: declared, Queues: map[string]string{"resize": "q.fifo"}}}})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(rendered)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Topics["resize"]; !reflect.DeepEqual(got.Declared, declared) || got.Queues["resize"] != "q.fifo" {
		t.Errorf("Parse(Render()) = %+v, want the declaration and queue it was rendered with", got)
	}
	if empty, err := Render(Topology{}); err != nil || empty != nil {
		t.Errorf("Render of a deploy with no topic = %q, %v, want no file", empty, err)
	}
}
