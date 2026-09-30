package switchboard_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func tunneledRouting(t *testing.T, upstreams map[string]string, tunneled ...string) []byte {
	t.Helper()
	var table map[string]any
	if err := json.Unmarshal(routing(t, upstreams), &table); err != nil {
		t.Fatal(err)
	}
	held := make([]map[string]string, 0, len(tunneled))
	for _, hostname := range tunneled {
		held = append(held, map[string]string{"hostname": hostname, "owner": "ocel--site-0--production"})
	}
	table["tunneled"] = held
	written, err := json.Marshal(table)
	if err != nil {
		t.Fatal(err)
	}
	return written
}

type tunnelBoard struct {
	data, https, tunnel string
	front               *http.Client
}

func servedWithTunnel(t *testing.T, document []byte) tunnelBoard {
	t.Helper()
	board, data, front := fronted(t, document)
	https, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tunnel, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = board.ServeHTTPS(https) }()
	go func() { _ = board.ServeTunnel(tunnel) }()
	return tunnelBoard{data: data, https: https.Addr().String(), tunnel: tunnel.Addr().String(), front: front}
}

func noRedirects() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func sent(t *testing.T, client *http.Client, at, host, path string, header http.Header) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://"+at+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = host
	for name, values := range header {
		request.Header[name] = values
	}
	answer, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = answer.Body.Close() })
	return answer
}

func TestATunneledHostnameIsForwardedWhenItArrivesThroughTheTunnel(t *testing.T) {
	t.Parallel()
	web := backend(t, "web")
	board := servedWithTunnel(t, tunneledRouting(t, map[string]string{"shop.example.com": web}, "shop.example.com"))

	answer := sent(t, noRedirects(), board.tunnel, "shop.example.com", "/cart", http.Header{
		"X-Forwarded-Proto": {"https"},
		"Cf-Connecting-Ip":  {"198.51.100.20"},
		"X-Forwarded-For":   {"198.51.100.20"},
	})

	said, _ := io.ReadAll(answer.Body)
	if answer.StatusCode != http.StatusOK || string(said) != "web" {
		t.Fatalf("the tunnel was answered %d %q, want the hostname's upstream", answer.StatusCode, said)
	}
	if got := answer.Header.Get("Seen-X-Forwarded-Proto"); got != "https" {
		t.Errorf("the upstream saw X-Forwarded-Proto %q, want https: Cloudflare says how the visitor reached it", got)
	}
	if got := answer.Header.Get("Seen-X-Forwarded-For"); got != "198.51.100.20" {
		t.Errorf("the upstream saw X-Forwarded-For %q, want the visitor Cloudflare names", got)
	}
	if got := answer.Header.Get("Seen-X-Forwarded-Host"); got != "shop.example.com" {
		t.Errorf("the upstream saw X-Forwarded-Host %q, want the hostname", got)
	}
}

func TestATunneledHostnameIsRefusedWhenItArrivesAnyOtherWay(t *testing.T) {
	t.Parallel()
	web := backend(t, "web")
	board := servedWithTunnel(t, tunneledRouting(t, map[string]string{"shop.example.com": web}, "shop.example.com"))

	for way, answer := range map[string]*http.Response{
		"the data listener":  sent(t, noRedirects(), board.data, "shop.example.com", "/", nil),
		"the https listener": sent(t, noRedirects(), board.https, "shop.example.com", "/", nil),
		"the front socket":   sent(t, board.front, "switchboard", "shop.example.com", "/", nil),
	} {
		if answer.StatusCode != http.StatusMisdirectedRequest || answer.Header.Get("Seen-X-Forwarded-Proto") != "" {
			t.Errorf("a tunneled hostname arriving over %s was answered %d, want 421 and nothing forwarded: only the tunnel reaches it", way, answer.StatusCode)
		}
	}
}

func TestAHostnameThatIsNotTunneledIsRefusedThroughTheTunnel(t *testing.T) {
	t.Parallel()
	web := backend(t, "web")
	board := servedWithTunnel(t, tunneledRouting(t, map[string]string{"shop.example.com": web, "blog.example.com": web}, "shop.example.com"))

	answer := sent(t, noRedirects(), board.tunnel, "blog.example.com", "/", http.Header{"X-Forwarded-Proto": {"https"}})

	if answer.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("a hostname the tunnel does not reach was answered %d through it, want 421: the proxy on the box answers it", answer.StatusCode)
	}
}

func TestAPlainHTTPVisitorThroughTheTunnelIsRedirectedToHTTPS(t *testing.T) {
	t.Parallel()
	web := backend(t, "web")
	board := servedWithTunnel(t, tunneledRouting(t, map[string]string{"shop.example.com": web}, "shop.example.com"))

	for said, header := range map[string]http.Header{
		"X-Forwarded-Proto": {"X-Forwarded-Proto": {"http"}},
		"CF-Visitor":        {"Cf-Visitor": {`{"scheme":"http"}`}},
	} {
		answer := sent(t, noRedirects(), board.tunnel, "shop.example.com", "/cart?item=1", header)
		if answer.StatusCode != http.StatusPermanentRedirect || answer.Header.Get("Location") != "https://shop.example.com/cart?item=1" {
			t.Errorf("a plain http visitor named by %s was answered %d to %q, want 308 to https://shop.example.com/cart?item=1", said, answer.StatusCode, answer.Header.Get("Location"))
		}
		if answer.Header.Get("Seen-X-Forwarded-Proto") != "" {
			t.Errorf("a plain http visitor named by %s was forwarded, want it only redirected", said)
		}
	}
}

func TestAPreviewUnderATunneledWildcardIsForwardedThroughTheTunnelOnly(t *testing.T) {
	t.Parallel()
	web := backend(t, "web")
	board := servedWithTunnel(t, tunneledRouting(t, map[string]string{"pr-1.preview.example.com": web}, "*.preview.example.com"))

	through := sent(t, noRedirects(), board.tunnel, "pr-1.preview.example.com", "/", http.Header{"X-Forwarded-Proto": {"https"}})
	around := sent(t, noRedirects(), board.https, "pr-1.preview.example.com", "/", nil)

	if through.StatusCode != http.StatusOK || around.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("a preview under a tunneled wildcard was answered %d through the tunnel and %d around it, want 200 and 421", through.StatusCode, around.StatusCode)
	}
}

func TestATunneledHostnameIsNeverAdmittedForACertificate(t *testing.T) {
	t.Parallel()
	table, err := switchboard.Read(tunneledRouting(t, map[string]string{"shop.example.com": "127.0.0.1:9", "blog.example.com": "127.0.0.1:9"}, "shop.example.com"))
	if err != nil {
		t.Fatal(err)
	}

	if table.Admits("shop.example.com") || !table.Admits("blog.example.com") {
		t.Errorf("Admits(shop) = %v, Admits(blog) = %v, want only blog admitted: the proxy on the box never answers a tunneled hostname, so it asks for no certificate for it", table.Admits("shop.example.com"), table.Admits("blog.example.com"))
	}
}
