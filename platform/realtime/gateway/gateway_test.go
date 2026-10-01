package gateway_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ocelhq/ocel/platform/realtime/gateway"
	"github.com/ocelhq/ocel/platform/realtime/token"
	"github.com/ocelhq/ocel/platform/realtime/token/tokentest"
)

const (
	host      = "realtime.shop.example"
	namespace = "app"
	appOrigin = "https://shop.example"
)

type harness struct {
	t      *testing.T
	key    ed25519.PrivateKey
	server *httptest.Server
	client *http.Client
	clock  atomic.Int64
}

func (h *harness) now() time.Time { return time.Unix(h.clock.Load(), 0) }

func (h *harness) advance(by time.Duration) { h.clock.Add(int64(by / time.Second)) }

type pipeListener struct {
	accepted chan net.Conn
	closed   chan struct{}
	once     sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{accepted: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.accepted:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

func (l *pipeListener) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	server, client := net.Pipe()
	select {
	case l.accepted <- server:
		return client, nil
	case <-l.closed:
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }

func (pipeAddr) String() string { return "pipe" }

func newHarness(t *testing.T, configure ...func(*gateway.Config)) *harness {
	t.Helper()
	h := newUnstartedHarness(t, configure...)
	h.client = http.DefaultClient
	h.server.Start()
	return h
}

func newUnbufferedHarness(t *testing.T, configure ...func(*gateway.Config)) *harness {
	t.Helper()
	h := newUnstartedHarness(t, configure...)
	listener := newPipeListener()
	_ = h.server.Listener.Close()
	h.server.Listener = listener
	transport := &http.Transport{DialContext: listener.DialContext}
	t.Cleanup(transport.CloseIdleConnections)
	h.client = &http.Client{Transport: transport}
	h.server.Start()
	return h
}

func newUnstartedHarness(t *testing.T, configure ...func(*gateway.Config)) *harness {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, key: private}
	h.clock.Store(1_790_000_000)
	cfg := gateway.Config{
		Host: host,
		Keys: func(requested string) (ed25519.PublicKey, bool) {
			return public, requested == namespace
		},
		AllowedOrigins: func() []string { return []string{appOrigin} },
		Now:            h.now,
	}
	for _, change := range configure {
		change(&cfg)
	}
	h.server = httptest.NewUnstartedServer(gateway.New(cfg))
	t.Cleanup(h.server.Close)
	return h
}

func (h *harness) mint(operation token.Operation, channel string) string {
	return tokentest.Sign(h.t, h.key, token.Claims{
		Audience:  host,
		ExpiresAt: h.now().Add(time.Minute).Unix(),
		IssuedAt:  h.now().Unix(),
		Issuer:    "ocel:rt:" + namespace,
		ID:        "jti-1",
		Subject:   "user-1",
		Ocel:      token.Grant{Channel: channel, Namespace: namespace, Operation: operation},
	})
}

func authorization(t *testing.T, tok string) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"host": host, "Authorization": tok})
	if err != nil {
		t.Fatal(err)
	}
	return "header-" + base64.RawURLEncoding.EncodeToString(header)
}

type client struct {
	t    *testing.T
	conn *websocket.Conn
}

func (h *harness) dial(connectToken string) (*client, *http.Response, error) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.server.URL, "http")+"/event/realtime", &websocket.DialOptions{
		Subprotocols: []string{"aws-appsync-event-ws", authorization(h.t, connectToken)},
		HTTPHeader:   http.Header{"Origin": {appOrigin}},
		HTTPClient:   h.client,
	})
	if err != nil {
		return nil, resp, err
	}
	h.t.Cleanup(func() { _ = conn.CloseNow() })
	return &client{t: h.t, conn: conn}, resp, nil
}

func (h *harness) connect() *client {
	h.t.Helper()
	c, _, err := h.dial(h.mint(token.Connect, "/"+namespace))
	if err != nil {
		h.t.Fatalf("dial the gateway: %v", err)
	}
	c.send(map[string]any{"type": "connection_init"})
	if ack := c.read(); ack["type"] != "connection_ack" {
		h.t.Fatalf("after connection_init got %v, want connection_ack", ack)
	}
	return c
}

func (c *client) send(frame map[string]any) {
	c.t.Helper()
	raw, err := json.Marshal(frame)
	if err != nil {
		c.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageText, raw); err != nil {
		c.t.Fatalf("send %s: %v", raw, err)
	}
}

func (c *client) read() map[string]any {
	c.t.Helper()
	frame, err := c.next(5 * time.Second)
	if err != nil {
		c.t.Fatalf("read a frame: %v", err)
	}
	return frame
}

func (c *client) next(within time.Duration) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	_, raw, err := c.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		c.t.Fatalf("decode frame %s: %v", raw, err)
	}
	return frame, nil
}

func (c *client) subscribe(id, channel, tok string) map[string]any {
	c.t.Helper()
	c.send(map[string]any{"type": "subscribe", "id": id, "channel": channel, "authorization": map[string]string{"Authorization": tok}})
	return c.read()
}

func event(t *testing.T, data any) string {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func (h *harness) publish(tok, channel string, events ...string) (*http.Response, map[string]any) {
	h.t.Helper()
	body, err := json.Marshal(map[string]any{"channel": channel, "events": events})
	if err != nil {
		h.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.server.URL+"/event", bytes.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", tok)
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("publish: %v", err)
	}
	defer resp.Body.Close()
	var answer map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&answer)
	return resp, answer
}

func TestAServerPublishReachesASubscriberAsTheEventItSent(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	c := h.connect()
	channel := "/app/orders/o-1"
	if got := c.subscribe("s-1", channel, h.mint(token.Subscribe, channel)); got["type"] != "subscribe_success" || got["id"] != "s-1" {
		t.Fatalf("subscribe answered %v, want subscribe_success for s-1", got)
	}

	sent := event(t, map[string]string{"status": "shipped"})
	resp, answer := h.publish(h.mint(token.Publish, channel), channel, sent)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("publish answered %s %v, want 200", resp.Status, answer)
	}

	got := c.read()
	if got["type"] != "data" || got["id"] != "s-1" || got["event"] != sent {
		t.Fatalf("subscriber got %v, want a data frame for s-1 carrying %s", got, sent)
	}
}

func TestASubscriberThatStopsReadingIsDisconnectedWithoutHoldingUpThePublisher(t *testing.T) {
	t.Parallel()

	const budgetBytes = 4 << 10
	h := newUnbufferedHarness(t, func(cfg *gateway.Config) { cfg.QueueBudgetBytes = budgetBytes })
	channel := "/app/feed"
	stalled := h.connect()
	stalled.subscribe("s-1", channel, h.mint(token.Subscribe, channel))
	healthy := h.connect()
	healthy.subscribe("s-2", channel, h.mint(token.Subscribe, channel))

	sent := event(t, strings.Repeat("x", 1<<10))
	published := 4 * budgetBytes / len(sent)
	for i := range published {
		if resp, answer := h.publish(h.mint(token.Publish, channel), channel, sent); resp.StatusCode != http.StatusOK {
			t.Fatalf("publish %d answered %s %v while a subscriber stopped reading, want 200", i, resp.Status, answer)
		}
		if got := healthy.read(); got["type"] != "data" || got["id"] != "s-2" || got["event"] != sent {
			t.Fatalf("after publish %d the subscriber still reading got %v, want a data frame for s-2", i, got)
		}
	}

	delivered := 0
	for {
		frame, err := stalled.next(5 * time.Second)
		if err != nil {
			if status := websocket.CloseStatus(err); status != websocket.StatusTryAgainLater {
				t.Fatalf("the subscriber that stopped reading ended with %v (status %d), want close status %d", err, status, websocket.StatusTryAgainLater)
			}
			break
		}
		if frame["type"] != "data" {
			t.Fatalf("the subscriber that stopped reading got %v, want only data frames before its close", frame)
		}
		delivered++
	}
	if delivered >= published {
		t.Fatalf("the subscriber that stopped reading was delivered all %d events, want the overflow past its %d byte budget dropped", published, budgetBytes)
	}
}

func TestAConnectionWhoseTokenWasMintedForAnotherGatewayIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	foreign := tokentest.Sign(t, h.key, token.Claims{
		Audience:  "realtime.other.example",
		ExpiresAt: h.now().Add(time.Minute).Unix(),
		Ocel:      token.Grant{Channel: "/" + namespace, Namespace: namespace, Operation: token.Connect},
	})

	c, _, err := h.dial(foreign)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if got := c.read(); got["type"] != "connection_error" {
		t.Fatalf("got %v, want connection_error", got)
	}
	if _, err := c.next(5 * time.Second); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("after connection_error the read ended with %v, want the socket closed for policy", err)
	}
}

func TestASubscribeTokenIsRefusedOnAnyChannelButTheOneItNames(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	c := h.connect()

	got := c.subscribe("s-1", "/app/orders/o-2", h.mint(token.Subscribe, "/app/orders/o-1"))
	if got["type"] != "subscribe_error" || got["id"] != "s-1" {
		t.Fatalf("subscribe answered %v, want subscribe_error for s-1", got)
	}

	h.publish(h.mint(token.Publish, "/app/orders/o-2"), "/app/orders/o-2", event(t, "secret"))
	if frame, err := c.next(200 * time.Millisecond); err == nil {
		t.Fatalf("a refused subscription received %v", frame)
	}
}

func TestAnExpiredSubscribeTokenIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	c := h.connect()
	stale := h.mint(token.Subscribe, "/app/orders/o-1")
	h.advance(2 * time.Minute)

	if got := c.subscribe("s-1", "/app/orders/o-1", stale); got["type"] != "subscribe_error" {
		t.Fatalf("subscribe answered %v, want subscribe_error", got)
	}
}

func TestASocketFromAnOriginNotAllowedIsRefusedBeforeItOpens(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.server.URL, "http")+"/event/realtime", &websocket.DialOptions{
		Subprotocols: []string{"aws-appsync-event-ws", authorization(t, h.mint(token.Connect, "/"+namespace))},
		HTTPHeader:   http.Header{"Origin": {"https://evil.example"}},
	})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("dial from another origin = %v, want 403", err)
	}
}

func TestTheTwoHundredAndFirstSubscriptionOnAConnectionIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	c := h.connect()
	channel := "/app/status"
	for i := range 200 {
		if got := c.subscribe(fmt.Sprintf("s-%d", i), channel, h.mint(token.Subscribe, channel)); got["type"] != "subscribe_success" {
			t.Fatalf("subscription %d answered %v, want subscribe_success", i, got)
		}
	}

	got := c.subscribe("one-too-many", channel, h.mint(token.Subscribe, channel))
	if got["type"] != "subscribe_error" {
		t.Fatalf("subscription 201 answered %v, want subscribe_error", got)
	}
}

func TestAConnectionIsAcknowledgedWithItsTimeoutAndKeptAlive(t *testing.T) {
	t.Parallel()

	h := newHarness(t, func(cfg *gateway.Config) { cfg.KeepAlive = 50 * time.Millisecond })
	c, _, err := h.dial(h.mint(token.Connect, "/"+namespace))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.send(map[string]any{"type": "connection_init"})

	ack := c.read()
	if ack["type"] != "connection_ack" || ack["connectionTimeoutMs"] != float64(250) {
		t.Fatalf("got %v, want connection_ack with a timeout of five keep-alives", ack)
	}
	if ka := c.read(); ka["type"] != "ka" {
		t.Fatalf("got %v, want a keep-alive", ka)
	}
}

func TestAnUnsubscribedChannelDeliversNothingMore(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	c := h.connect()
	channel := "/app/orders/o-1"
	c.subscribe("s-1", channel, h.mint(token.Subscribe, channel))

	c.send(map[string]any{"type": "unsubscribe", "id": "s-1"})
	if got := c.read(); got["type"] != "unsubscribe_success" || got["id"] != "s-1" {
		t.Fatalf("unsubscribe answered %v, want unsubscribe_success for s-1", got)
	}

	h.publish(h.mint(token.Publish, channel), channel, event(t, "late"))
	if frame, err := c.next(200 * time.Millisecond); err == nil {
		t.Fatalf("an unsubscribed connection received %v", frame)
	}
}

func TestABrowserCannotPublishOverTheSocket(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	c := h.connect()
	channel := "/app/rooms/r-1"

	c.send(map[string]any{"type": "publish", "id": "p-1", "channel": channel, "events": []string{event(t, "hi")}, "authorization": map[string]string{"Authorization": h.mint(token.Publish, channel)}})

	if got := c.read(); got["type"] != "publish_error" || got["id"] != "p-1" {
		t.Fatalf("publish answered %v, want publish_error for p-1", got)
	}
}

func TestAServerPublishWithASubscribeTokenIsUnauthorized(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	channel := "/app/orders/o-1"

	resp, answer := h.publish(h.mint(token.Subscribe, channel), channel, event(t, "forged"))

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("publish answered %s %v, want 401", resp.Status, answer)
	}
}

func TestAServerPublishRelaysAnyJSONEventAndRefusesOneOverTheSizeLimitOrNotJSON(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	c := h.connect()
	channel := "/app/orders/o-1"
	c.subscribe("s-1", channel, h.mint(token.Subscribe, channel))

	oversized := event(t, strings.Repeat("x", gateway.MaxEventBytes))
	object := `{"ch":"/app/orders/o-2","anything":true}`
	bare := `"bare"`
	resp, answer := h.publish(h.mint(token.Publish, channel), channel, oversized, object, "not json", bare)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("publish answered %s %v, want 200", resp.Status, answer)
	}

	failed, _ := answer["failed"].([]any)
	successful, _ := answer["successful"].([]any)
	if len(failed) != 2 || len(successful) != 2 {
		t.Fatalf("publish answered %v, want the oversized and the non-JSON event failed and the other two published", answer)
	}
	for _, published := range successful {
		if identifier, _ := published.(map[string]any)["identifier"].(string); identifier == "" {
			t.Fatalf("publish answered %v, want every published event given an identifier", answer)
		}
	}
	if got := c.read(); got["event"] != object {
		t.Fatalf("subscriber got %v, want %s as published", got, object)
	}
	if got := c.read(); got["event"] != bare {
		t.Fatalf("subscriber got %v, want %s as published", got, bare)
	}
}

func TestUnsubscribingAnUnknownIDAnswersTheErrorAppSyncSends(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	c := h.connect()

	c.send(map[string]any{"type": "unsubscribe", "id": "s-9"})

	got := c.read()
	errs, _ := got["errors"].([]any)
	if got["type"] != "unsubscribe_error" || got["id"] != "s-9" || len(errs) != 1 {
		t.Fatalf("unsubscribe answered %v, want one unsubscribe_error for s-9", got)
	}
	want := map[string]any{"errorType": "UnknownOperationError", "message": "Unknown operation id s-9"}
	if first, _ := errs[0].(map[string]any); first["errorType"] != want["errorType"] || first["message"] != want["message"] {
		t.Fatalf("unsubscribe_error carried %v, want %v", errs[0], want)
	}
}

func TestAWildcardSubscriptionOnANamespaceReceivesAPublishBelowIt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	c := h.connect()
	if got := c.subscribe("s-1", "/app/*", h.mint(token.Subscribe, "/app/*")); got["type"] != "subscribe_success" {
		t.Fatalf("subscribe answered %v, want subscribe_success", got)
	}

	channel := "/app/orders/o-1"
	sent := event(t, "shipped")
	h.publish(h.mint(token.Publish, channel), channel, sent)

	if got := c.read(); got["type"] != "data" || got["id"] != "s-1" || got["event"] != sent {
		t.Fatalf("subscriber got %v, want a data frame for s-1 carrying %s", got, sent)
	}
}
