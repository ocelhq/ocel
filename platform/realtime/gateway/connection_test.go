package gateway

import (
	"crypto/ed25519"
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/realtime/token"
	"github.com/ocelhq/ocel/platform/realtime/token/tokentest"
)

func TestASubscriptionIsConfirmedOnlyOnceItReceivesWhatIsPublished(t *testing.T) {
	t.Parallel()

	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_790_000_000, 0)
	g := New(Config{
		Host: "realtime.shop.example",
		Keys: func(namespace string) (ed25519.PublicKey, bool) { return public, namespace == "app" },
		Now:  func() time.Time { return now },
	})
	c := &connection{g: g, namespace: "app", queue: newQueue(DefaultQueueBudgetBytes), subscriptions: map[string]func(){}}
	channel := "/app/orders/o-1"
	subscribe := clientFrame{Type: frameSubscribe, ID: "s-1", Channel: channel, Authorization: authorization{Token: tokentest.Sign(t, private, token.Claims{
		Audience:  "realtime.shop.example",
		ExpiresAt: now.Add(time.Minute).Unix(),
		Ocel:      token.Grant{Channel: channel, Namespace: "app", Operation: token.Subscribe},
	})}}

	g.hub.mu.RLock()
	subscribed := make(chan struct{})
	go func() {
		defer close(subscribed)
		c.subscribe(subscribe)
	}()
	for g.hub.mu.TryRLock() {
		g.hub.mu.RUnlock()
		runtime.Gosched()
	}
	queuedBeforeRegistering := c.queue.Take()
	g.hub.mu.RUnlock()
	<-subscribed
	if len(queuedBeforeRegistering) != 0 {
		t.Fatalf("the connection queued %s before the hub held its subscription, want subscribe_success only once a publish reaches it", queuedBeforeRegistering)
	}

	g.hub.Publish(channel, `{"status":"shipped"}`)
	var got []idFrame
	for _, raw := range c.queue.Take() {
		var frame idFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("decode frame %s: %v", raw, err)
		}
		got = append(got, frame)
	}
	if len(got) != 2 || got[0] != (idFrame{Type: frameSubscribeSuccess, ID: "s-1"}) || got[1] != (idFrame{Type: frameData, ID: "s-1"}) {
		t.Fatalf("frames = %+v, want subscribe_success for s-1 then its data", got)
	}
}
