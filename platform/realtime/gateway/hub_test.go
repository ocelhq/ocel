package gateway_test

import (
	"encoding/json"
	"testing"

	"github.com/ocelhq/ocel/platform/realtime/gateway"
)

type dataFrame struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Event string `json:"event"`
}

func takeData(t *testing.T, queue *gateway.Queue) []dataFrame {
	t.Helper()
	var out []dataFrame
	for _, frame := range queue.Take() {
		var data dataFrame
		if err := json.Unmarshal(frame, &data); err != nil {
			t.Fatalf("decode frame %s: %v", frame, err)
		}
		out = append(out, data)
	}
	return out
}

func TestAPublishReachesEverySubscriptionOnItsChannelAndNoOther(t *testing.T) {
	t.Parallel()

	hub := gateway.NewHub()
	mine, theirs := gateway.NewQueue(1<<20), gateway.NewQueue(1<<20)
	hub.Subscribe("/app/orders/o-1", "a", mine)
	hub.Subscribe("/app/orders/o-1", "b", mine)
	hub.Subscribe("/app/orders/o-2", "c", theirs)

	hub.Publish("/app/orders/o-1", `{"n":1}`)

	got := takeData(t, mine)
	if len(got) != 2 || got[0].Type != "data" || got[0].Event != `{"n":1}` || got[0].ID == got[1].ID {
		t.Fatalf("frames = %+v, want one data frame for each of a and b", got)
	}
	if other := theirs.Take(); len(other) != 0 {
		t.Fatalf("o-2's subscriber got %d frames from a publish on o-1", len(other))
	}
}

func TestASubscriberThatFallsBehindItsBudgetOverflowsWithoutHoldingUpAnother(t *testing.T) {
	t.Parallel()

	hub := gateway.NewHub()
	slow, fast := gateway.NewQueue(200), gateway.NewQueue(1<<20)
	hub.Subscribe("/app/status", "slow", slow)
	hub.Subscribe("/app/status", "fast", fast)

	for range 10 {
		hub.Publish("/app/status", `{"status":"a fifty byte event, give or take"}`)
	}

	select {
	case <-slow.Overflowed():
	default:
		t.Fatal("a subscriber ten events behind on a 200 byte budget has not overflowed")
	}
	if frames := slow.Take(); len(frames) != 0 {
		t.Fatalf("an overflowed subscriber still holds %d frames, want them dropped with the connection", len(frames))
	}
	if got := takeData(t, fast); len(got) != 10 {
		t.Fatalf("the subscriber keeping up got %d events, want all 10", len(got))
	}
}

func TestAWildcardSubscriptionReceivesEveryChannelBelowItsPrefixAndNotThePrefixItself(t *testing.T) {
	t.Parallel()

	hub := gateway.NewHub()
	queue := gateway.NewQueue(1 << 20)
	hub.Subscribe("/app/projects/p-1/deploys/*", "w", queue)

	hub.Publish("/app/projects/p-1/deploys/d-1", `"below"`)
	hub.Publish("/app/projects/p-1/deploys/d-1/logs", `"deeper"`)
	hub.Publish("/app/projects/p-1/deploys", `"prefix"`)
	hub.Publish("/app/projects/p-2/deploys/d-1", `"another project"`)

	got := takeData(t, queue)
	if len(got) != 2 || got[0].Event != `"below"` || got[1].Event != `"deeper"` {
		t.Fatalf("frames = %+v, want the two events below the prefix", got)
	}
}
