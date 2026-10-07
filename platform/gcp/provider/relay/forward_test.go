package relay_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/relay"
)

func echoing(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String()
}

func relayAllowing(t *testing.T, allowed ...string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(relay.NewHandler(destinationsOf(t, allowed...)))
	t.Cleanup(server.Close)
	return server
}

func destinationsOf(t *testing.T, targets ...string) []relay.Destination {
	t.Helper()
	destinations := make([]relay.Destination, len(targets))
	for i, target := range targets {
		address, err := netip.ParseAddrPort(target)
		if err != nil {
			t.Fatal(err)
		}
		destinations[i] = relay.Destination{Network: netip.PrefixFrom(address.Addr(), address.Addr().BitLen()), Port: address.Port()}
	}
	return destinations
}

func tokenOf(value string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return value, nil }
}

func roundTrip(t *testing.T, address, message string) string {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("dial the forward: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(message)); err != nil {
		t.Fatalf("write through the forward: %v", err)
	}
	got := make([]byte, len(message))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read through the forward: %v", err)
	}
	return string(got)
}

func TestBytesWrittenToAForwardReachTheTargetAndTheAnswerComesBack(t *testing.T) {
	t.Parallel()
	target := echoing(t)
	server := relayAllowing(t, target)

	forward, err := relay.OpenForward(context.Background(), relay.Link{URL: server.URL, Target: target, Token: tokenOf("id-token")})
	if err != nil {
		t.Fatalf("OpenForward() = %v", err)
	}
	defer forward.Close()

	if !strings.HasPrefix(forward.Address(), "127.0.0.1:") {
		t.Errorf("Address() = %q, want a loopback port", forward.Address())
	}
	if got := roundTrip(t, forward.Address(), "hello, database"); got != "hello, database" {
		t.Errorf("the target answered %q, want its own echo", got)
	}
}

func TestATargetOutsideTheAllowlistIsRefusedByTheRelayBeforeAnyConnectionIsMade(t *testing.T) {
	t.Parallel()
	allowed, forbidden := echoing(t), echoing(t)
	server := relayAllowing(t, allowed)

	_, err := relay.OpenForward(context.Background(), relay.Link{URL: server.URL, Target: forbidden, Token: tokenOf("id-token")})

	if err == nil {
		t.Fatal("OpenForward() to a target the relay does not allow = nil error")
	}
	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeNotReady {
		t.Errorf("OpenForward() = %v, want a not-ready refusal so the build goes without the binding", err)
	}
}

func TestAForwardPresentsAFreshIdentityTokenOnEveryConnectionItTunnels(t *testing.T) {
	t.Parallel()
	target := echoing(t)
	var mutex sync.Mutex
	var presented []string
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		presented = append(presented, r.Header.Get("Authorization"))
		mutex.Unlock()
		relay.NewHandler(destinationsOf(t, target)).ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)
	var minted int
	token := func(context.Context) (string, error) {
		mutex.Lock()
		defer mutex.Unlock()
		minted++
		return "token-" + string(rune('a'+minted-1)), nil
	}

	forward, err := relay.OpenForward(context.Background(), relay.Link{URL: front.URL, Target: target, Token: token})
	if err != nil {
		t.Fatalf("OpenForward() = %v", err)
	}
	defer forward.Close()
	roundTrip(t, forward.Address(), "one")
	roundTrip(t, forward.Address(), "two")

	mutex.Lock()
	defer mutex.Unlock()
	if len(presented) != 3 {
		t.Fatalf("the front end saw %v, want the readiness probe and both connections", presented)
	}
	for _, header := range presented {
		if !strings.HasPrefix(header, "Bearer token-") {
			t.Errorf("a request carried Authorization %q, want a bearer identity token", header)
		}
	}
}

func TestClosingAForwardEndsItsOpenConnectionsAndFreesItsPort(t *testing.T) {
	t.Parallel()
	target := echoing(t)
	server := relayAllowing(t, target)
	forward, err := relay.OpenForward(context.Background(), relay.Link{URL: server.URL, Target: target, Token: tokenOf("id-token")})
	if err != nil {
		t.Fatalf("OpenForward() = %v", err)
	}
	open, err := net.Dial("tcp", forward.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	_ = open.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := open.Write([]byte("warm")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(open, make([]byte, 4)); err != nil {
		t.Fatal(err)
	}

	forward.Close()
	forward.Close()

	if _, err := net.DialTimeout("tcp", forward.Address(), time.Second); err == nil {
		t.Error("the forward's port still accepts connections after Close()")
	}
	if _, err := open.Read(make([]byte, 1)); err == nil {
		t.Error("a connection open at Close() still reads, want it ended")
	}
}

func TestATargetTheRelayCannotReachIsRefusedAsNotReady(t *testing.T) {
	t.Parallel()
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	target := closed.Addr().String()
	_ = closed.Close()
	server := relayAllowing(t, target)

	_, err = relay.OpenForward(context.Background(), relay.Link{URL: server.URL, Target: target, Token: tokenOf("id-token")})

	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeNotReady {
		t.Errorf("OpenForward() to a target nothing listens on = %v, want a not-ready refusal", err)
	}
}

func TestAnIdentityTokenNeverReachesARefusalMessage(t *testing.T) {
	t.Parallel()
	target := echoing(t)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	t.Cleanup(front.Close)

	_, err := relay.OpenForward(context.Background(), relay.Link{URL: front.URL, Target: target, Token: tokenOf("secret-bad-token")})

	if err == nil {
		t.Fatal("OpenForward() with an identity the front end rejects = nil error")
	}
	if strings.Contains(err.Error(), "secret-bad-token") {
		t.Errorf("the refusal %q carries the identity token", err)
	}
}
