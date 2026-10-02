package realtime_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/devresources/realtime"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/platform/realtime/gateway"
	"github.com/ocelhq/ocel/platform/realtime/token"
	"github.com/ocelhq/ocel/platform/realtime/token/tokentest"
)

const appOrigin = "http://localhost:3000"

func declared(name string) declaration.Resource {
	return declaration.Resource{Name: name, Type: resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME, Realtime: &resourcesv1.RealtimeConfig{}}
}

func newBackend(t *testing.T) *realtime.Backend {
	t.Helper()
	backend := realtime.New(func() []string { return []string{appOrigin} })
	t.Cleanup(func() { _ = backend.Close(context.Background(), true) })
	return backend
}

func resolveOne(t *testing.T, backend *realtime.Backend, name string) *bindingsv1.RealtimeProperties {
	t.Helper()
	resolved, err := backend.Resolve(context.Background(), "shop-1a2b", []declaration.Resource{declared(name)})
	if err != nil {
		t.Fatalf("Resolve = %v", err)
	}
	if len(resolved) != 1 || len(resolved[0].Env) != 1 {
		t.Fatalf("Resolve = %+v, want one binding", resolved)
	}
	var raw string
	for _, value := range resolved[0].Env {
		raw = value
	}
	var b bindingsv1.Binding
	if err := protojson.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatalf("the binding is not the JSON the SDKs read: %v\n%s", err, raw)
	}
	return b.GetRealtime()
}

func mint(t *testing.T, bound *bindingsv1.RealtimeProperties, namespace string, operation token.Operation, channel string) string {
	t.Helper()
	return tokentest.Sign(t, ed25519.NewKeyFromSeed(bound.GetSigningKey()), token.Claims{
		Audience:  bound.GetHost(),
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
		Subject:   "server",
		Ocel:      token.Grant{Channel: channel, Namespace: namespace, Operation: operation},
	})
}

func dial(t *testing.T, bound *bindingsv1.RealtimeProperties, connectToken string) (*websocket.Conn, error) {
	t.Helper()
	header, err := json.Marshal(map[string]string{"host": bound.GetHost(), "Authorization": connectToken})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, bound.GetUrl(), &websocket.DialOptions{
		Subprotocols: []string{gateway.Subprotocol, "header-" + base64.RawURLEncoding.EncodeToString(header)},
		HTTPHeader:   http.Header{"Origin": {appOrigin}},
	})
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn, nil
}

func exchange(t *testing.T, conn *websocket.Conn, frame map[string]any) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if frame != nil {
		raw, _ := json.Marshal(frame)
		if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
			t.Fatalf("send %s: %v", raw, err)
		}
	}
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return got
}

func TestAResourceKeepsItsKeyWhileDeclaredAndItsTokensAreRefusedOnceItIsNot(t *testing.T) {
	t.Parallel()

	backend := newBackend(t)
	bound := resolveOne(t, backend, "app")
	if again := resolveOne(t, backend, "app"); !bytes.Equal(again.GetSigningKey(), bound.GetSigningKey()) || again.GetHost() != bound.GetHost() {
		t.Fatal("resolving the same declaration again changed its key or gateway, want the app's tokens to stay valid")
	}

	if _, err := backend.Resolve(context.Background(), "shop-1a2b", nil); err != nil {
		t.Fatalf("Resolve(nothing) = %v", err)
	}

	conn, err := dial(t, bound, mint(t, bound, "app", token.Connect, "/app"))
	if err != nil {
		t.Fatalf("dial the gateway: %v", err)
	}
	if got := exchange(t, conn, nil); got["type"] != "connection_error" {
		t.Fatalf("got %v, want a connection for an undeclared resource refused", got)
	}
}

func TestTheBindingCarriesAGatewayOnLoopbackThatAcceptsTheTokensItsKeySigns(t *testing.T) {
	t.Parallel()

	bound := resolveOne(t, newBackend(t), "app")
	if bound.GetTransport() != bindingsv1.RealtimeTransport_REALTIME_TRANSPORT_OCEL_GATEWAY {
		t.Errorf("transport = %s, want the Ocel gateway", bound.GetTransport())
	}
	if !strings.HasPrefix(bound.GetUrl(), "ws://127.0.0.1:") || !strings.HasPrefix(bound.GetHost(), "127.0.0.1:") {
		t.Fatalf("url %q host %q, want the gateway on loopback", bound.GetUrl(), bound.GetHost())
	}
	public := ed25519.NewKeyFromSeed(bound.GetSigningKey()).Public().(ed25519.PublicKey)
	if !bytes.Equal(public, bound.GetVerifyKey()) {
		t.Fatal("the verify key is not the signing key's public key")
	}

	conn, err := dial(t, bound, mint(t, bound, "app", token.Connect, "/app"))
	if err != nil {
		t.Fatalf("dial the gateway: %v", err)
	}
	if got := exchange(t, conn, map[string]any{"type": "connection_init"}); got["type"] != "connection_ack" {
		t.Fatalf("got %v, want connection_ack", got)
	}
	channel := "/app/orders/o-1"
	subscribe := map[string]any{"type": "subscribe", "id": "s-1", "channel": channel, "authorization": map[string]string{"Authorization": mint(t, bound, "app", token.Subscribe, channel)}}
	if got := exchange(t, conn, subscribe); got["type"] != "subscribe_success" {
		t.Fatalf("got %v, want subscribe_success", got)
	}

	event := `{"v":1,"id":"0123456789abcdef0123456789abcdef","ch":"/app/orders/o-1","ts":1790000000000,"kind":"live","data":{"status":"shipped"}}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+bound.GetHost()+"/publish", strings.NewReader(event))
	req.Header.Set("Authorization", "Bearer "+mint(t, bound, "app", token.Publish, channel))
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

func TestASocketOpenWhenTheBackendClosesIsClosed(t *testing.T) {
	t.Parallel()

	backend := newBackend(t)
	bound := resolveOne(t, backend, "app")
	conn, err := dial(t, bound, mint(t, bound, "app", token.Connect, "/app"))
	if err != nil {
		t.Fatalf("dial the gateway: %v", err)
	}
	if got := exchange(t, conn, map[string]any{"type": "connection_init"}); got["type"] != "connection_ack" {
		t.Fatalf("got %v, want connection_ack", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	readEnded := make(chan error, 1)
	go func() {
		_, _, err := conn.Read(ctx)
		readEnded <- err
	}()

	if err := backend.Close(ctx, true); err != nil {
		t.Fatalf("Close = %v", err)
	}

	if err := <-readEnded; websocket.CloseStatus(err) != websocket.StatusGoingAway {
		t.Fatalf("after Close the read ended with %v, want the socket closed as going away", err)
	}
}
