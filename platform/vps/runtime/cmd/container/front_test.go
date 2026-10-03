package main

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/platform/realtime/gateway"
	variables "github.com/ocelhq/ocel/platform/vps/provider/live"
)

type heard struct {
	path   string
	origin string
	secret string
}

func serveGateway(t *testing.T) (string, chan heard) {
	t.Helper()
	seen := make(chan heard, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- heard{path: r.URL.Path, origin: r.Header.Get("Origin"), secret: r.Header.Get(originguard.OriginSecretHeader)}
		if r.Header.Get("Upgrade") != "websocket" {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		conn, written, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = written.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\nheard")
		_ = written.Flush()
	}))
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://"), seen
}

func realtimeManifest(publishURL string) variables.Manifest {
	return variables.Manifest{Slug: "shop", Tier: "production", RealtimePublishURL: publishURL}
}

func mustFront(t *testing.T, manifest variables.Manifest, opts originguard.Options) http.Handler {
	t.Helper()
	front, err := newFront(manifest, opts)
	if err != nil {
		t.Fatalf("newFront() = %v", err)
	}
	return front
}

func serveApp(t *testing.T) *url.URL {
	t.Helper()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Answered-By", "app")
	}))
	t.Cleanup(app.Close)
	upstream, _ := url.Parse(app.URL)
	return upstream
}

func upgrade(t *testing.T, front, origin string) string {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(front, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	request := "GET " + variables.RealtimeSocketPath + " HTTP/1.1\r\nHost: web.shop.example\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nOrigin: " + origin + "\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read the answer to the upgrade: %v", err)
	}
	return status
}

func TestTheRealtimePathOnTheAppsOriginIsForwardedToTheGatewayTheRuntimePublishesTo(t *testing.T) {
	address, seen := serveGateway(t)
	front := httptest.NewServer(mustFront(t, realtimeManifest("http://"+address+gateway.PublishPath), originguard.Options{Upstream: serveApp(t)}))
	t.Cleanup(front.Close)

	if status := upgrade(t, front.URL, "https://web.shop.example"); !strings.Contains(status, "101") {
		t.Fatalf("the upgrade was answered %q, want the gateway's 101", status)
	}
	got := <-seen
	if got.path != gateway.SocketPath {
		t.Errorf("the gateway was asked for %q, want its socket at %q", got.path, gateway.SocketPath)
	}
	if got.origin != "" {
		t.Errorf("the gateway was handed Origin %q: the handler judged the origin when it minted the tokens, and the gateway trusts no origin of its own", got.origin)
	}

	res, err := http.Get(front.URL + "/orders")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.Header.Get("X-Answered-By") != "app" {
		t.Errorf("a request off the realtime path was answered %s by something other than the app", res.Status)
	}
}

func TestTheRealtimePathIsGuardedAsTheAppIs(t *testing.T) {
	address, seen := serveGateway(t)
	front := httptest.NewServer(mustFront(t, realtimeManifest("http://"+address+gateway.PublishPath),
		originguard.Options{Upstream: serveApp(t), Guard: originguard.NewGuard("edge-secret")}))
	t.Cleanup(front.Close)

	if status := upgrade(t, front.URL, "https://web.shop.example"); !strings.Contains(status, "403") {
		t.Errorf("an upgrade without the edge's secret was answered %q, want 403: the gateway is reached only the way the app is", status)
	}
	select {
	case got := <-seen:
		t.Errorf("the gateway heard %+v from a request the edge never sent", got)
	default:
	}
}

func TestAContainerHandedNoGatewayLeavesTheRealtimePathToTheApp(t *testing.T) {
	_, seen := serveGateway(t)
	front := httptest.NewServer(mustFront(t, realtimeManifest(""), originguard.Options{Upstream: serveApp(t)}))
	t.Cleanup(front.Close)

	res, err := http.Get(front.URL + variables.RealtimeSocketPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.Header.Get("X-Answered-By") != "app" {
		t.Errorf("the realtime path was answered %s by something other than the app, though no gateway serves it", res.Status)
	}
	select {
	case got := <-seen:
		t.Errorf("the gateway heard %+v", got)
	default:
	}
}

func TestAGatewayAddressThatNamesNoGatewayKeepsTheFrontFromStarting(t *testing.T) {
	for name, publishURL := range map[string]string{
		"a path with no host": gateway.PublishPath,
		"no url at all":       "http://%zz",
	} {
		if _, err := newFront(realtimeManifest(publishURL), originguard.Options{Upstream: serveApp(t)}); err == nil {
			t.Errorf("newFront() with %s = nil, want an error rather than the realtime path silently answered by the app", name)
		}
	}
}
