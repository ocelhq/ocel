package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ocelhq/ocel/platform/realtime/gateway"
	"github.com/ocelhq/ocel/platform/realtime/gatewayenv"
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
	cfg, err := readConfig(environOf(map[string]string{gatewayenv.HostVar: audience, gatewayenv.KeysVar: keysOf(t, keys)}), time.Now)
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
	keysFile := filepath.Join(t.TempDir(), "keys.json")
	writeKeysFile(t, keysFile, map[string]ed25519.PrivateKey{"app": newKey(t)})
	for name, env := range map[string]map[string]string{
		"no host":               {gatewayenv.KeysVar: valid},
		"no keys":               {gatewayenv.HostVar: audience},
		"keys that are no JSON": {gatewayenv.HostVar: audience, gatewayenv.KeysVar: "app=abc"},
		"a key of 3 bytes":      {gatewayenv.HostVar: audience, gatewayenv.KeysVar: `{"app":"AAEC"}`},
		"a key not base64":      {gatewayenv.HostVar: audience, gatewayenv.KeysVar: `{"app":"not base64!"}`},
		"an empty key set":      {gatewayenv.HostVar: audience, gatewayenv.KeysVar: `{}`},
		"keys and a keys file":  {gatewayenv.HostVar: audience, gatewayenv.KeysVar: valid, gatewayenv.KeysFileVar: keysFile},
		"a missing keys file":   {gatewayenv.HostVar: audience, gatewayenv.KeysFileVar: filepath.Join(t.TempDir(), "absent.json")},
		"a socket cap of 0":     {gatewayenv.HostVar: audience, gatewayenv.KeysVar: valid, gatewayenv.MaxSocketsVar: "0"},
		"a socket cap of words": {gatewayenv.HostVar: audience, gatewayenv.KeysVar: valid, gatewayenv.MaxSocketsVar: "many"},
		"any origin of maybe":   {gatewayenv.HostVar: audience, gatewayenv.KeysVar: valid, gatewayenv.AnyOriginVar: "maybe"},
	} {
		if _, err := readConfig(environOf(env), time.Now); err == nil {
			t.Errorf("readConfig with %s = nil, want it refused", name)
		}
	}
}

type fakeClock struct{ at atomic.Int64 }

func (c *fakeClock) now() time.Time { return time.Unix(0, c.at.Load()) }

func (c *fakeClock) advance(by time.Duration) { c.at.Add(int64(by)) }

func writeKeysFile(t *testing.T, path string, keys map[string]ed25519.PrivateKey) {
	t.Helper()
	if err := os.WriteFile(path, []byte(keysOf(t, keys)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func startGatewayFrom(t *testing.T, env map[string]string, clock *fakeClock) string {
	t.Helper()
	env[gatewayenv.HostVar] = audience
	now := time.Now
	if clock != nil {
		now = clock.now
	}
	cfg, err := readConfig(environOf(env), now)
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
	t.Cleanup(func() {
		cancel()
		<-served
	})
	return listener.Addr().String()
}

func dialFrom(t *testing.T, address, origin, connectToken string) (map[string]any, int) {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"Authorization": connectToken})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws://"+address+gateway.SocketPath, &websocket.DialOptions{
		Subprotocols: []string{gateway.Subprotocol, "header-" + base64.RawURLEncoding.EncodeToString(header)},
		HTTPHeader:   http.Header{"Origin": {origin}},
	})
	if err != nil {
		if resp == nil {
			t.Fatalf("dial the gateway: %v", err)
		}
		return nil, resp.StatusCode
	}
	defer conn.CloseNow()
	return exchange(t, conn, map[string]any{"type": "connection_init"}), http.StatusSwitchingProtocols
}

func TestAKeyAddedToTheKeysFileIsTrustedWithoutARestart(t *testing.T) {
	t.Parallel()

	app, chat := newKey(t), newKey(t)
	path := filepath.Join(t.TempDir(), "keys.json")
	writeKeysFile(t, path, map[string]ed25519.PrivateKey{"app": app})
	clock := &fakeClock{}
	address := startGatewayFrom(t, map[string]string{gatewayenv.KeysFileVar: path}, clock)

	if _, ack := connect(t, address, mint(t, app, "app", token.Connect, "/app", "u1")); ack["type"] != "connection_ack" {
		t.Fatalf("connect to app = %v, want connection_ack", ack)
	}
	writeKeysFile(t, path, map[string]ed25519.PrivateKey{"app": app, "chat": chat})
	clock.advance(time.Second)
	if _, ack := connect(t, address, mint(t, chat, "chat", token.Connect, "/chat", "u1")); ack["type"] != "connection_ack" {
		t.Fatalf("connect to chat once its key is in the file = %v, want connection_ack", ack)
	}
}

func TestAnUnknownNamespaceRereadsTheKeysFileAtMostOnceASecond(t *testing.T) {
	t.Parallel()

	app, chat := newKey(t), newKey(t)
	path := filepath.Join(t.TempDir(), "keys.json")
	writeKeysFile(t, path, map[string]ed25519.PrivateKey{"app": app})
	clock := &fakeClock{}
	address := startGatewayFrom(t, map[string]string{gatewayenv.KeysFileVar: path}, clock)

	clock.advance(time.Second)
	if _, refused := connect(t, address, mint(t, chat, "chat", token.Connect, "/chat", "u1")); refused["type"] == "connection_ack" {
		t.Fatalf("connect to chat before its key is in the file = %v, want it refused", refused)
	}
	writeKeysFile(t, path, map[string]ed25519.PrivateKey{"app": app, "chat": chat})
	if _, refused := connect(t, address, mint(t, chat, "chat", token.Connect, "/chat", "u1")); refused["type"] == "connection_ack" {
		t.Fatalf("connect to chat within a second of the last read = %v, want it refused", refused)
	}
	clock.advance(time.Second)
	if _, ack := connect(t, address, mint(t, chat, "chat", token.Connect, "/chat", "u1")); ack["type"] != "connection_ack" {
		t.Fatalf("connect to chat a second later = %v, want connection_ack", ack)
	}
}

func TestAKeyRemovedFromTheKeysFileStopsBeingTrustedWithinThirtySeconds(t *testing.T) {
	t.Parallel()

	app, chat := newKey(t), newKey(t)
	path := filepath.Join(t.TempDir(), "keys.json")
	writeKeysFile(t, path, map[string]ed25519.PrivateKey{"app": app, "chat": chat})
	clock := &fakeClock{}
	address := startGatewayFrom(t, map[string]string{gatewayenv.KeysFileVar: path}, clock)

	writeKeysFile(t, path, map[string]ed25519.PrivateKey{"app": app})
	if _, ack := connect(t, address, mint(t, chat, "chat", token.Connect, "/chat", "u1")); ack["type"] != "connection_ack" {
		t.Fatalf("connect to chat before the keys are read again = %v, want connection_ack", ack)
	}
	clock.advance(30 * time.Second)
	if _, refused := connect(t, address, mint(t, chat, "chat", token.Connect, "/chat", "u1")); refused["type"] == "connection_ack" {
		t.Fatalf("connect to chat 30s after its key left the file = %v, want it refused", refused)
	}
}

func TestAKeysFileThatCannotBeReadKeepsTheKeysLastRead(t *testing.T) {
	t.Parallel()

	app := newKey(t)
	path := filepath.Join(t.TempDir(), "keys.json")
	writeKeysFile(t, path, map[string]ed25519.PrivateKey{"app": app})
	clock := &fakeClock{}
	address := startGatewayFrom(t, map[string]string{gatewayenv.KeysFileVar: path}, clock)

	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	clock.advance(30 * time.Second)
	if _, ack := connect(t, address, mint(t, app, "app", token.Connect, "/app", "u1")); ack["type"] != "connection_ack" {
		t.Fatalf("connect to app while the keys file is malformed = %v, want connection_ack", ack)
	}
}

func TestASocketFromAnotherOriginOpensOnlyWhenTheGatewayAllowsAnyOrigin(t *testing.T) {
	t.Parallel()

	key := newKey(t)
	keys := keysOf(t, map[string]ed25519.PrivateKey{"app": key})
	connectToken := mint(t, key, "app", token.Connect, "/app", "u1")

	strict := startGatewayFrom(t, map[string]string{gatewayenv.KeysVar: keys}, nil)
	if _, status := dialFrom(t, strict, "https://shop.example", connectToken); status != http.StatusForbidden {
		t.Errorf("dial from another origin = %d, want 403", status)
	}
	open := startGatewayFrom(t, map[string]string{gatewayenv.KeysVar: keys, gatewayenv.AnyOriginVar: "true"}, nil)
	if ack, _ := dialFrom(t, open, "https://shop.example", connectToken); ack["type"] != "connection_ack" {
		t.Errorf("dial from another origin = %v, want connection_ack", ack)
	}
}

func TestASocketPastTheConfiguredCapIsRefusedWithServiceUnavailable(t *testing.T) {
	t.Parallel()

	key := newKey(t)
	address := startGatewayFrom(t, map[string]string{gatewayenv.KeysVar: keysOf(t, map[string]ed25519.PrivateKey{"app": key}), gatewayenv.MaxSocketsVar: "1"}, nil)

	connect(t, address, mint(t, key, "app", token.Connect, "/app", "u1"))
	if _, status := dialFrom(t, address, "", mint(t, key, "app", token.Connect, "/app", "u2")); status != http.StatusServiceUnavailable {
		t.Errorf("dial past the cap = %d, want 503", status)
	}
}

func TestTheGatewayListensOnPort8080UnlessTold(t *testing.T) {
	t.Parallel()

	keys := keysOf(t, map[string]ed25519.PrivateKey{"app": newKey(t)})
	cfg, err := readConfig(environOf(map[string]string{gatewayenv.HostVar: audience, gatewayenv.KeysVar: keys}), time.Now)
	if err != nil || cfg.listen != ":8080" {
		t.Errorf("listen = %q (%v), want :8080", cfg.listen, err)
	}
	cfg, err = readConfig(environOf(map[string]string{gatewayenv.HostVar: audience, gatewayenv.KeysVar: keys, listenEnv: "127.0.0.1:9000"}), time.Now)
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
