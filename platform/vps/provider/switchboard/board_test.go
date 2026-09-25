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
	"slices"
	"strings"
	"sync"
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

func standing(t *testing.T, document []byte, trusted ...netip.Prefix) (*switchboard.Board, string) {
	t.Helper()
	return trusting(t, document, switchboard.Trust{Prefixes: trusted})
}

func trusting(t *testing.T, document []byte, trust switchboard.Trust) (*switchboard.Board, string) {
	t.Helper()
	table, err := switchboard.Read(document)
	if err != nil {
		t.Fatal(err)
	}
	board := switchboard.New(table, trust)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = board.Serve(listener) }()
	t.Cleanup(func() { _ = board.Close() })
	return board, listener.Addr().String()
}

func backend(t *testing.T, name string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(edge.HeaderEdge, "app")
		w.Header().Set("X-Served-Host", r.Host)
		w.Header().Set("X-Served-Path", r.URL.Path)
		for _, forwarded := range []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host"} {
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

func TestForwardedHeadersAClientSpoofsAreOverwrittenAndOnlyATrustedFrontProxysAreKept(t *testing.T) {
	t.Parallel()

	web := backend(t, "web")
	spoofed := []string{"X-Forwarded-For", "6.6.6.6", "X-Forwarded-Proto", "https", "X-Forwarded-Host", "bank.example.com"}

	_, untrusting := standing(t, routing(t, map[string]string{"shop.example.com": web}), netip.MustParsePrefix("10.9.0.0/16"))
	said := ask(t, http.DefaultClient, untrusting, "shop.example.com", "/", spoofed...)
	for header, want := range map[string]string{
		"X-Forwarded-For":   "127.0.0.1",
		"X-Forwarded-Proto": "http",
		"X-Forwarded-Host":  "shop.example.com",
	} {
		if got := said.header.Get("Seen-" + header); got != want {
			t.Errorf("an untrusted peer's %s reached the upstream as %q, want %q: anything a client says about where it came from is a lie until a trusted proxy says it", header, got, want)
		}
	}

	_, trusting := standing(t, routing(t, map[string]string{"shop.example.com": web}), netip.MustParsePrefix("127.0.0.1/32"))
	said = ask(t, http.DefaultClient, trusting, "shop.example.com", "/", spoofed...)
	for header, want := range map[string]string{
		"X-Forwarded-For":   "6.6.6.6, 127.0.0.1",
		"X-Forwarded-Proto": "https",
		"X-Forwarded-Host":  "bank.example.com",
	} {
		if got := said.header.Get("Seen-" + header); got != want {
			t.Errorf("the trusted front proxy's %s reached the upstream as %q, want %q: it terminated tls and knows the client", header, got, want)
		}
	}
}

func TestAFrontProxyTrustedByNameIsTrustedAtWhateverAddressItsNameResolvesToNow(t *testing.T) {
	t.Parallel()

	web := backend(t, "web")
	var mu sync.Mutex
	resolved := netip.MustParseAddr("10.9.0.7")
	asked := 0
	board, at := trusting(t, routing(t, map[string]string{"shop.example.com": web}), switchboard.Trust{
		Names: []string{"ocel-proxy"},
	})
	board.LookUpNamesWith(func(_ context.Context, name string) ([]netip.Addr, error) {
		mu.Lock()
		defer mu.Unlock()
		asked++
		if name != "ocel-proxy" {
			return nil, fmt.Errorf("asked to resolve %q", name)
		}
		return []netip.Addr{resolved}, nil
	})
	board.RefreshTrustEvery(50 * time.Millisecond)
	spoofed := []string{"X-Forwarded-Proto", "https"}

	if said := ask(t, http.DefaultClient, at, "shop.example.com", "/", spoofed...); said.header.Get("Seen-X-Forwarded-Proto") != "http" {
		t.Errorf("a peer the trusted name does not resolve to had its X-Forwarded-Proto kept as %q, want http", said.header.Get("Seen-X-Forwarded-Proto"))
	}
	mu.Lock()
	resolved = netip.MustParseAddr("127.0.0.1")
	mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for said := ask(t, http.DefaultClient, at, "shop.example.com", "/", spoofed...); said.header.Get("Seen-X-Forwarded-Proto") != "https"; said = ask(t, http.DefaultClient, at, "shop.example.com", "/", spoofed...) {
		if time.Now().After(deadline) {
			t.Fatal("the front proxy's name came to resolve to the peer and its X-Forwarded-Proto was still overwritten five seconds later: a recreated front proxy takes a new address, and every app behind it would be told its https clients came over http")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	before := asked
	mu.Unlock()
	for range 20 {
		ask(t, http.DefaultClient, at, "shop.example.com", "/", spoofed...)
	}
	mu.Lock()
	defer mu.Unlock()
	if asked != before {
		t.Errorf("twenty requests from the trusted peer asked the resolver %d more times, want none: a peer already trusted is answered from what the name last resolved to", asked-before)
	}
}
