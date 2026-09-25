package switchboard_test

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func TestAPeerTheResolverIsSlowToPlaceDoesNotHoldUpTheTrustedFrontProxy(t *testing.T) {
	t.Parallel()

	web := backend(t, "web")
	stalled := make(chan struct{})
	t.Cleanup(func() { close(stalled) })
	var mu sync.Mutex
	asked := 0
	board, at := trusting(t, routing(t, map[string]string{"shop.example.com": web}), switchboard.Trust{
		Names: []string{"ocel-proxy"},
	})
	board.LookUpNamesWith(func(ctx context.Context, _ string) ([]netip.Addr, error) {
		mu.Lock()
		asked++
		first := asked == 1
		mu.Unlock()
		if !first {
			select {
			case <-stalled:
			case <-ctx.Done():
			}
			return nil, ctx.Err()
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	})
	board.RefreshTrustEvery(0)
	spoofed := []string{"X-Forwarded-Proto", "https"}
	if said := ask(t, http.DefaultClient, at, "shop.example.com", "/", spoofed...); said.header.Get("Seen-X-Forwarded-Proto") != "https" {
		t.Fatalf("the front proxy's first request kept X-Forwarded-Proto as %q, want https", said.header.Get("Seen-X-Forwarded-Proto"))
	}

	stranger := &http.Client{Transport: &http.Transport{DialContext: (&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP("127.0.0.2")}}).DialContext}}
	strange, err := http.NewRequest(http.MethodGet, "http://"+at+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	strange.Host = "shop.example.com"
	go func() {
		if answer, err := stranger.Do(strange); err == nil {
			_ = answer.Body.Close()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		stalling := asked > 1
		mu.Unlock()
		if stalling {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a peer the name does not resolve to never set the resolver looking again")
		}
		time.Sleep(time.Millisecond)
	}

	quick := &http.Client{Timeout: 500 * time.Millisecond}
	began := time.Now()
	said := ask(t, quick, at, "shop.example.com", "/", spoofed...)
	if said.header.Get("Seen-X-Forwarded-Proto") != "https" {
		t.Errorf("the trusted front proxy was answered in %s with X-Forwarded-Proto %q while the resolver was stalled for a stranger, want https inside half a second: every request the box serves comes through it, and one stranger's lookup must not hold up any of them",
			time.Since(began).Round(time.Millisecond), said.header.Get("Seen-X-Forwarded-Proto"))
	}
}
