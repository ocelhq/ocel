package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ocelhq/ocel/platform/realtime/gateway"
	"github.com/ocelhq/ocel/platform/realtime/token"
	"github.com/ocelhq/ocel/platform/realtime/token/tokentest"
)

const audience = "shop-production-realtime-gateway:8080"

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func environOf(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func keysOf(t *testing.T, keys map[string]ed25519.PrivateKey) string {
	t.Helper()
	encoded := map[string]string{}
	for namespace, key := range keys {
		encoded[namespace] = base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
	}
	raw, err := json.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func startGateway(t *testing.T, keys map[string]ed25519.PrivateKey) (string, func() error) {
	t.Helper()
	cfg, err := readConfig(environOf(map[string]string{hostEnv: audience, keysEnv: keysOf(t, keys)}))
	if err != nil {
		t.Fatalf("readConfig = %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- serve(ctx, listener, cfg) }()
	stop := sync.OnceValue(func() error {
		cancel()
		return <-served
	})
	t.Cleanup(func() { _ = stop() })
	return listener.Addr().String(), stop
}

func mint(t *testing.T, key ed25519.PrivateKey, namespace string, operation token.Operation, channel, subject string) string {
	t.Helper()
	return tokentest.Sign(t, key, token.Claims{
		Audience:  audience,
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
		Subject:   subject,
		Ocel:      token.Grant{Channel: channel, Namespace: namespace, Operation: operation},
	})
}

func connect(t *testing.T, address, connectToken string) (*websocket.Conn, map[string]any) {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"Authorization": connectToken})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws://"+address+gateway.SocketPath, &websocket.DialOptions{
		Subprotocols: []string{gateway.Subprotocol, "header-" + base64.RawURLEncoding.EncodeToString(header)},
	})
	if err != nil {
		t.Fatalf("dial the gateway: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn, exchange(t, conn, map[string]any{"type": "connection_init"})
}

func exchange(t *testing.T, conn *websocket.Conn, frame map[string]any) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if frame != nil {
		raw, _ := json.Marshal(frame)
		if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
			return map[string]any{"type": "closed", "error": err.Error()}
		}
	}
	_, raw, err := conn.Read(ctx)
	if err != nil {
		return map[string]any{"type": "closed", "error": err.Error()}
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return got
}

func TestTheGatewayServesEachNamespaceItHasAKeyForAndRefusesAnyOther(t *testing.T) {
	t.Parallel()

	app, chat, stranger := newKey(t), newKey(t), newKey(t)
	address, _ := startGateway(t, map[string]ed25519.PrivateKey{"app": app, "chat": chat})

	for namespace, key := range map[string]ed25519.PrivateKey{"app": app, "chat": chat} {
		if _, ack := connect(t, address, mint(t, key, namespace, token.Connect, "/"+namespace, "u1")); ack["type"] != "connection_ack" {
			t.Errorf("connect to %s = %v, want connection_ack", namespace, ack)
		}
	}
	for namespace, key := range map[string]ed25519.PrivateKey{"app": stranger, "other": stranger} {
		if _, refused := connect(t, address, mint(t, key, namespace, token.Connect, "/"+namespace, "u1")); refused["type"] == "connection_ack" {
			t.Errorf("connect to %s signed by an unknown key = %v, want it refused", namespace, refused)
		}
	}
}

func TestAServerPublishReachesTheSubscribersOfItsChannel(t *testing.T) {
	t.Parallel()

	key := newKey(t)
	address, _ := startGateway(t, map[string]ed25519.PrivateKey{"app": key})
	conn, _ := connect(t, address, mint(t, key, "app", token.Connect, "/app", "u1"))
	channel := "/app/orders/o-1"
	subscribe := map[string]any{"type": "subscribe", "id": "s-1", "channel": channel, "authorization": map[string]string{"Authorization": mint(t, key, "app", token.Subscribe, channel, "u1")}}
	if got := exchange(t, conn, subscribe); got["type"] != "subscribe_success" {
		t.Fatalf("subscribe = %v, want subscribe_success", got)
	}

	event := `{"v":1,"id":"0123456789abcdef0123456789abcdef","ch":"/app/orders/o-1","ts":1790000000000,"kind":"live","data":{"status":"shipped"}}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+address+gateway.PublishPath, strings.NewReader(event))
	req.Header.Set("Authorization", "Bearer "+mint(t, key, "app", token.Publish, channel, "server"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("publish answered %s, want 204", resp.Status)
	}
	if got := exchange(t, conn, nil); got["type"] != "data" || got["event"] != event {
		t.Fatalf("got %v, want the published event", got)
	}
}

func TestStoppingTheGatewayClosesItsSocketsGoingAway(t *testing.T) {
	t.Parallel()

	key := newKey(t)
	address, stop := startGateway(t, map[string]ed25519.PrivateKey{"app": key})
	conn, _ := connect(t, address, mint(t, key, "app", token.Connect, "/app", "u1"))

	stopped := make(chan error, 1)
	go func() { stopped <- stop() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusGoingAway {
		t.Errorf("read after stop = %v, want the socket closed going away so its client reconnects", err)
	}
	if err := <-stopped; err != nil {
		t.Errorf("serve = %v, want nil once stopped", err)
	}
}

func TestAConfigTheGatewayCannotVerifyTokensWithIsRefused(t *testing.T) {
	t.Parallel()

	valid := keysOf(t, map[string]ed25519.PrivateKey{"app": newKey(t)})
	for name, env := range map[string]map[string]string{
		"no host":               {keysEnv: valid},
		"no keys":               {hostEnv: audience},
		"keys that are no JSON": {hostEnv: audience, keysEnv: "app=abc"},
		"a key of 3 bytes":      {hostEnv: audience, keysEnv: `{"app":"AAEC"}`},
		"a key not base64":      {hostEnv: audience, keysEnv: `{"app":"not base64!"}`},
		"an empty key set":      {hostEnv: audience, keysEnv: `{}`},
	} {
		if _, err := readConfig(environOf(env)); err == nil {
			t.Errorf("readConfig with %s = nil, want it refused", name)
		}
	}
}

func TestTheGatewayListensOnPort8080UnlessTold(t *testing.T) {
	t.Parallel()

	keys := keysOf(t, map[string]ed25519.PrivateKey{"app": newKey(t)})
	cfg, err := readConfig(environOf(map[string]string{hostEnv: audience, keysEnv: keys}))
	if err != nil || cfg.listen != ":8080" {
		t.Errorf("listen = %q (%v), want :8080", cfg.listen, err)
	}
	cfg, err = readConfig(environOf(map[string]string{hostEnv: audience, keysEnv: keys, listenEnv: "127.0.0.1:9000"}))
	if err != nil || cfg.listen != "127.0.0.1:9000" {
		t.Errorf("listen = %q (%v), want 127.0.0.1:9000", cfg.listen, err)
	}
}

func TestReadyAnswersOnlyWhileTheGatewayListens(t *testing.T) {
	t.Parallel()

	address, stop := startGateway(t, map[string]ed25519.PrivateKey{"app": newKey(t)})
	if err := probeReady(address); err != nil {
		t.Errorf("probeReady while listening = %v, want nil", err)
	}
	if err := stop(); err != nil {
		t.Fatalf("stop = %v", err)
	}
	if err := probeReady(address); err == nil {
		t.Error("probeReady once stopped = nil, want an error")
	}
}

func TestReadyFailsWhenWhatAnswersOnItsPortIsNoGateway(t *testing.T) {
	t.Parallel()

	stranger := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(stranger.Close)
	if err := probeReady(stranger.Listener.Addr().String()); err == nil {
		t.Error("probeReady against a server that is no gateway = nil, want an error")
	}

	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = silent.Close() })
	go func() {
		for {
			conn, err := silent.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	if err := probeReady(silent.Addr().String()); err == nil {
		t.Error("probeReady against a port that accepts and answers nothing = nil, want an error")
	}
}
