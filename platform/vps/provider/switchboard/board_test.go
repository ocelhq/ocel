package switchboard_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func routing(t *testing.T, upstreams map[string]string) []byte {
	t.Helper()
	type claim struct {
		Owner    string `json:"owner"`
		Hostname string `json:"hostname"`
		Pointer  string `json:"pointer"`
	}
	type route struct {
		Owner    string `json:"owner"`
		Pointer  string `json:"pointer"`
		App      string `json:"app"`
		Upstream string `json:"upstream"`
	}
	table := struct {
		Grace  string  `json:"grace"`
		Claims []claim `json:"claims"`
		Routes []route `json:"routes"`
	}{Grace: "30s"}
	for at, hostname := range slices.Sorted(maps.Keys(upstreams)) {
		owner := fmt.Sprintf("ocel--site-%d--production", at)
		table.Claims = append(table.Claims, claim{Owner: owner, Hostname: hostname, Pointer: "@production"})
		table.Routes = append(table.Routes, route{Owner: owner, Pointer: "@production", App: "web", Upstream: upstreams[hostname]})
	}
	written, err := json.Marshal(table)
	if err != nil {
		t.Fatal(err)
	}
	return written
}

func standing(t *testing.T, document []byte) (*switchboard.Board, string) {
	t.Helper()
	board, at, _ := fronted(t, document)
	return board, at
}

func fronted(t *testing.T, document []byte, relayed ...netip.Prefix) (*switchboard.Board, string, *http.Client) {
	t.Helper()
	table, err := switchboard.Read(document)
	if err != nil {
		t.Fatal(err)
	}
	board := switchboard.New(table, relayed...)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "front.sock")
	if len(socket) > 100 {
		t.Skipf("a unix socket path this host accepts does not fit under %s", socket)
	}
	front, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = board.Serve(listener) }()
	go func() { _ = board.ServeFront(front) }()
	t.Cleanup(func() { _ = board.Close() })
	dialer := &net.Dialer{}
	return board, listener.Addr().String(), &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		},
	}}
}

func hearingHTTPS(t *testing.T, document []byte, relayed ...netip.Prefix) string {
	t.Helper()
	table, err := switchboard.Read(document)
	if err != nil {
		t.Fatal(err)
	}
	board := switchboard.New(table, relayed...)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = board.ServeHTTPS(listener) }()
	t.Cleanup(func() { _ = board.Close() })
	return listener.Addr().String()
}

func backend(t *testing.T, name string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(edge.HeaderEdge, "app")
		w.Header().Set("X-Served-Host", r.Host)
		w.Header().Set("X-Served-Path", r.URL.Path)
		for _, forwarded := range []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "X-Forwarded-Port", "X-Forwarded-Prefix", "Forwarded", "X_Forwarded_Proto", "X_Forwarded_Host", "X-Real-Ip", "True-Client-Ip"} {
			w.Header().Set("Seen-"+forwarded, strings.Join(r.Header.Values(forwarded), ","))
		}
		_, _ = io.WriteString(w, name)
	}))
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://")
}

type answered struct {
	status int
	body   string
	header http.Header
}

func ask(t *testing.T, client *http.Client, at, host, path string, headers ...string) answered {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://"+at+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = host
	for at := 0; at+1 < len(headers); at += 2 {
		request.Header.Set(headers[at], headers[at+1])
	}
	response, err := client.Do(request)
	if err != nil {
		t.Errorf("GET %s%s: %v", host, path, err)
		return answered{}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Errorf("GET %s%s: %v", host, path, err)
	}
	return answered{status: response.StatusCode, body: string(body), header: response.Header}
}

func switchedTo(t *testing.T, at, host, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for said := ask(t, http.DefaultClient, at, host, "/"); said.body != want; said = ask(t, http.DefaultClient, at, host, "/") {
		if time.Now().After(deadline) {
			t.Fatalf("%s still answered %q after five seconds, want %q", host, said.body, want)
		}
	}
}

func TestAClaimedHostnameIsServedByItsUpstreamUnderItsOwnHostAndNamesTheBox(t *testing.T) {
	t.Parallel()

	web := backend(t, "web")
	_, at := standing(t, routing(t, map[string]string{"shop.example.com": web}))

	said := ask(t, http.DefaultClient, at, "shop.example.com", "/cart")
	if said.status != http.StatusOK || said.body != "web" {
		t.Fatalf("shop.example.com answered %d %q, want the upstream's 200", said.status, said.body)
	}
	if got := said.header.Get("X-Served-Host"); got != "shop.example.com" {
		t.Errorf("the upstream was asked for host %q, want the hostname the client asked for", got)
	}
	if got := said.header.Get("X-Served-Path"); got != "/cart" {
		t.Errorf("the upstream was asked for %q, want /cart", got)
	}
	if got := said.header.Values(edge.HeaderEdge); !slices.Equal(got, []string{"box"}) {
		t.Errorf("the answer names the edge %v, want only box: the bind's probe reads this header off every hostname the box serves", got)
	}
}

func TestEveryAnswerTheBoxRefusesOrCannotReachNamesTheBox(t *testing.T) {
	t.Parallel()

	gone, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unreachable := gone.Addr().String()
	_ = gone.Close()
	_, at := standing(t, routing(t, map[string]string{"down.example.com": unreachable}))

	for host, status := range map[string]int{
		"unclaimed.example.com": http.StatusNotFound,
		"down.example.com":      http.StatusBadGateway,
	} {
		said := ask(t, http.DefaultClient, at, host, "/")
		if said.status != status || said.header.Get(edge.HeaderEdge) != "box" {
			t.Errorf("%s answered %d with %s %q, want %d naming box", host, said.status, edge.HeaderEdge, said.header.Get(edge.HeaderEdge), status)
		}
		if host == "unclaimed.example.com" && said.body != "" {
			t.Errorf("%s answered %q, want a bare 404 that says nothing about what else this box serves", host, said.body)
		}
	}
}

func TestForwardedHeadersAClientSpoofsAreOverwrittenAndOnlyTheFrontProxysAreKept(t *testing.T) {
	t.Parallel()

	web := backend(t, "web")
	spoofed := []string{"X-Forwarded-For", "6.6.6.6", "X-Forwarded-Proto", "https", "X-Forwarded-Host", "bank.example.com"}
	_, at, front := fronted(t, routing(t, map[string]string{"shop.example.com": web}))

	said := ask(t, http.DefaultClient, at, "shop.example.com", "/", spoofed...)
	for header, want := range map[string]string{
		"X-Forwarded-For":   "127.0.0.1",
		"X-Forwarded-Proto": "http",
		"X-Forwarded-Host":  "shop.example.com",
	} {
		if got := said.header.Get("Seen-" + header); got != want {
			t.Errorf("a peer on the switchboard's own listener had its %s reach the upstream as %q, want %q: anything a client says about where it came from is a lie until the front proxy says it", header, got, want)
		}
	}

	said = ask(t, front, "front", "shop.example.com", "/", spoofed...)
	for header, want := range map[string]string{
		"X-Forwarded-For":   "6.6.6.6",
		"X-Forwarded-Proto": "https",
		"X-Forwarded-Host":  "bank.example.com",
	} {
		if got := said.header.Get("Seen-" + header); got != want {
			t.Errorf("the front proxy's %s reached the upstream as %q, want %q: it terminated tls and knows the client, and it is trusted on its first request whatever address it was recreated at", header, got, want)
		}
	}
}

func TestARelayingPeerIsHeardOnTheSchemeAndHostButNeverOnTheClient(t *testing.T) {
	t.Parallel()

	web := backend(t, "web")
	spoofed := []string{"X-Forwarded-For", "6.6.6.6", "X-Forwarded-Proto", "https", "X-Forwarded-Host", "shop.example.com"}
	_, at, _ := fronted(t, routing(t, map[string]string{"shop.example.com": web}), netip.MustParsePrefix("127.0.0.0/8"))
	said := ask(t, http.DefaultClient, at, "shop.example.com", "/", spoofed...)
	for header, want := range map[string]string{
		"X-Forwarded-For":   "127.0.0.1",
		"X-Forwarded-Proto": "https",
		"X-Forwarded-Host":  "shop.example.com",
	} {
		if got := said.header.Get("Seen-" + header); got != want {
			t.Errorf("a relaying peer's %s reached the upstream as %q, want %q: every process on the box reaches the port your proxy relays through, so it is heard on the scheme it terminated and never on who the client is", header, got, want)
		}
	}
}

func TestTheLivenessProbeIsToldTheSchemeAndHostsAnAppWouldHear(t *testing.T) {
	t.Parallel()

	web := backend(t, "web")
	table := routing(t, map[string]string{"shop.example.com": web})
	_, relaying, _ := fronted(t, table, netip.MustParsePrefix("127.0.0.0/8"))
	_, untrusting, front := fronted(t, table)
	for name, tc := range map[string]struct {
		client    *http.Client
		at, host  string
		forwarded []string
		heard     string
	}{
		"a routed hostname from a relaying peer": {
			http.DefaultClient, relaying, "shop.example.com", []string{"X-Forwarded-Proto", "https"}, "https shop.example.com shop.example.com"},
		"an unclaimed hostname from a relaying peer": {
			http.DefaultClient, relaying, "unclaimed.example.com", []string{"X-Forwarded-Proto", "https"}, "https unclaimed.example.com unclaimed.example.com"},
		"a relaying peer forwarding another host": {
			http.DefaultClient, relaying, "shop.example.com", []string{"X-Forwarded-Proto", "https", "X-Forwarded-Host", "bank.example.com"}, "https shop.example.com bank.example.com"},
		"a routed hostname from the front proxy": {
			front, "front", "shop.example.com", []string{"X-Forwarded-Proto", "https"}, "https shop.example.com shop.example.com"},
		"a routed hostname from an untrusted peer": {
			http.DefaultClient, untrusting, "shop.example.com", []string{"X-Forwarded-Proto", "https", "X-Forwarded-Host", "bank.example.com"}, "http shop.example.com shop.example.com"},
	} {
		said := ask(t, tc.client, tc.at, tc.host, edge.LivenessProbePath, tc.forwarded...)
		if got := said.header.Get(switchboard.HeardHeader); got != tc.heard {
			t.Errorf("%s: the probe was told %q, want %q: the scheme, Host and X-Forwarded-Host the upstream would be sent", name, got, tc.heard)
		}
		if other := ask(t, tc.client, tc.at, tc.host, "/", tc.forwarded...); other.header.Get(switchboard.HeardHeader) != "" {
			t.Errorf("%s: a request off the probe path was told %q, want nothing said beyond the probe", name, other.header.Get(switchboard.HeardHeader))
		}
	}
}

func TestAPeerOnTheHTTPSListenerIsHeardAsHTTPSForTheHostItAskedAndOnNothingItSaid(t *testing.T) {
	t.Parallel()

	web := backend(t, "web")
	table := routing(t, map[string]string{"shop.example.com": web})
	spoofed := []string{
		"X-Forwarded-For", "6.6.6.6", "X-Forwarded-Proto", "http", "X-Forwarded-Host", "bank.example.com",
		"X-Forwarded-Port", "8443", "X-Forwarded-Prefix", "/admin", "Forwarded", "for=6.6.6.6;proto=http",
		"X_Forwarded_Proto", "http", "X_Forwarded_Host", "bank.example.com", "X-Real-Ip", "6.6.6.6", "True-Client-Ip", "6.6.6.6",
	}
	for name, at := range map[string]string{
		"a peer nothing relays from":   hearingHTTPS(t, table),
		"a peer the board relays from": hearingHTTPS(t, table, netip.MustParsePrefix("127.0.0.0/8")),
	} {
		said := ask(t, http.DefaultClient, at, "shop.example.com", "/", spoofed...)
		if said.status != http.StatusOK || said.body != "web" {
			t.Fatalf("%s: shop.example.com answered %d %q over the https listener, want the upstream's 200", name, said.status, said.body)
		}
		for header, want := range map[string]string{
			"X-Forwarded-For":    "127.0.0.1",
			"X-Forwarded-Proto":  "https",
			"X-Forwarded-Host":   "shop.example.com",
			"X-Forwarded-Port":   "",
			"X-Forwarded-Prefix": "",
			"Forwarded":          "",
			"X_Forwarded_Proto":  "",
			"X_Forwarded_Host":   "",
			"X-Real-Ip":          "",
			"True-Client-Ip":     "",
		} {
			if got := said.header.Get("Seen-" + header); got != want {
				t.Errorf("%s: %s reached the upstream as %q, want %q: only a proxy ocel writes https routes into reaches this listener, and every tenant on its network can too, so the scheme is stamped and nothing the peer says is heard", name, header, got, want)
			}
		}
		if heard := ask(t, http.DefaultClient, at, "shop.example.com", edge.LivenessProbePath, spoofed...).header.Get(switchboard.HeardHeader); heard != "https shop.example.com shop.example.com" {
			t.Errorf("%s: the probe was told %q, want https for the Host it asked", name, heard)
		}
	}
}

func TestEveryForwardedHeaderBeyondTheThreeTheBoardWritesIsDroppedWhoeverSentIt(t *testing.T) {
	t.Parallel()

	web := backend(t, "web")
	table := routing(t, map[string]string{"shop.example.com": web})
	spoofed := []string{
		"X-Forwarded-Port", "8443", "X-Forwarded-Prefix", "/admin", "Forwarded", "for=6.6.6.6;proto=https",
		"X_Forwarded_Proto", "https", "X_Forwarded_Host", "bank.example.com", "X-Real-Ip", "6.6.6.6", "True-Client-Ip", "6.6.6.6",
	}
	_, untrusting, front := fronted(t, table)
	_, relaying, _ := fronted(t, table, netip.MustParsePrefix("127.0.0.0/8"))
	for name, asked := range map[string]struct {
		client *http.Client
		at     string
	}{
		"an untrusted peer": {http.DefaultClient, untrusting},
		"a relaying peer":   {http.DefaultClient, relaying},
		"the front proxy":   {front, "front"},
	} {
		said := ask(t, asked.client, asked.at, "shop.example.com", "/", spoofed...)
		for _, header := range []string{"X-Forwarded-Port", "X-Forwarded-Prefix", "Forwarded", "X_Forwarded_Proto", "X_Forwarded_Host", "X-Real-Ip", "True-Client-Ip"} {
			if got := said.header.Get("Seen-" + header); got != "" {
				t.Errorf("%s had its %s reach the upstream as %q, want it dropped: the board vouches for the scheme, the host and the client, and an app that reads any other forwarded header, a CGI-style server that maps _ onto -, or a client-address header would be told what the peer chose", name, header, got)
			}
		}
	}
}
