package host

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func TestAClaimComposedOntoAFileOneDeployMovedOnceIsRetriedOntoWhatThatDeployLeft(t *testing.T) {
	t.Parallel()

	stood := claimingBox(t, routed())
	moved := mustWrite(t, twoProjects())
	proxied := servesProxy(stood.bench, &stood.held)
	writes := 0
	stood.answer = func(command string) (session.Result, bool) {
		if !writesProxy(command) {
			return proxied(command)
		}
		stood.mu.Lock()
		writes++
		first := writes == 1
		if first {
			stood.held = string(moved)
		}
		stood.mu.Unlock()
		if first {
			return session.Result{Code: routingMoved, Stderr: digested(string(moved))}, true
		}
		return proxied(command)
	}

	if err := stood.host().ClaimHosts(context.Background(), []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}); err != nil {
		t.Fatalf("ClaimHosts() beside one deploy that rewrote %s = %v: the compare-and-set exists to refuse a lost update, and a release retries it rather than handing the collision to the user, so a domain bind does the same", ProxyConfig, err)
	}
	if written := writes; written != 2 {
		t.Errorf("the claim wrote %s %d times, want twice: once onto the digest the other deploy moved, and once onto what it left", ProxyConfig, written)
	}
	held, err := ReadRoutingTable([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(held.Claims, []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}) {
		t.Errorf("%s holds claims %v after the retried claim, want %q claimed by %q", ProxyConfig, held.Claims, claimed, surface)
	}
	if len(held.Routes) != 2 {
		t.Errorf("%s holds routes %v after the retried claim, want both of the routes the deploy that moved it wrote: the retry composes onto what it re-read", ProxyConfig, held.Routes)
	}
}
