package box_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const tunnelEdge edge.Kind = "cloudflare"

type tunnelEvents struct {
	opened           []string
	held             map[string]string
	refusesDelete    error
	refusesConfigure error
	duringEnsure     func()
}

func TestATunnelTheEdgeFailedToDeleteIsDeletedByTheNextRelease(t *testing.T) {
	m, routed := routedOn(t)
	ctx := context.Background()
	if _, err := routed.Claim(ctx, tunneledClaim("shop.example.com")); err != nil {
		t.Fatal(err)
	}
	m.tunnelEvents.refusesDelete = errors.New("cloudflare answered 503")

	if err := routed.Disclaim(ctx, "shop.example.com"); err == nil || !strings.Contains(err.Error(), "uuid-1") {
		t.Fatalf("Disclaim with the delete refused = %v, want the tunnel it could not delete named", err)
	}
	if len(m.retired) != 1 {
		t.Fatalf("the box retires %+v, want the tunnel kept until the edge deletes it", m.retired)
	}
	m.tunnelEvents.refusesDelete = nil
	if err := routed.Disclaim(ctx, "shop.example.com"); err != nil {
		t.Fatalf("Disclaim again: %v", err)
	}

	if len(m.retired) != 0 || !slices.Contains(m.tunnelEvents.opened, "delete uuid-1") {
		t.Errorf("the box retires %+v and the edge saw %v, want the tunnel deleted and forgotten", m.retired, m.tunnelEvents.opened)
	}
}

func TestATunnelOpenedButNeverRunIsDeletedByItsNameWhenReleased(t *testing.T) {
	m, routed := routedOn(t)
	ctx := context.Background()
	m.tunnelEvents.refusesConfigure = errors.New("cloudflare answered 503")

	if _, err := routed.Claim(ctx, tunneledClaim("shop.example.com")); err == nil {
		t.Fatal("Claim with the configure refused succeeded")
	}
	if err := routed.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	if !slices.Contains(m.tunnelEvents.opened, "delete uuid-1") || len(m.retired) != 0 {
		t.Errorf("the edge saw %v and the box retires %+v, want the tunnel opened under the reserved name deleted: no id was recorded for it, and forgetting it leaves it at the edge", m.tunnelEvents.opened, m.retired)
	}
}

func TestATunnelOpenedUnderANameAnotherRunReleasedIsDeleted(t *testing.T) {
	m, routed := routedOn(t)
	m.tunnelEvents.duringEnsure = func() { m.tunnel = nil }

	_, err := routed.Claim(context.Background(), tunneledClaim("shop.example.com"))

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Fatalf("Claim through a tunnel released mid-claim = %v, want it refused busy", err)
	}
	if !slices.Contains(m.tunnelEvents.opened, "delete uuid-1") {
		t.Errorf("the edge saw %v, want the tunnel this claim opened deleted: nothing on the box names it any more, so no release ever would", m.tunnelEvents.opened)
	}
}

func (m *machine) openTunnels(kind edge.Kind) (*edge.TunnelHooks, error) {
	if kind != tunnelEdge {
		return nil, refusal.Refuse(refusal.CodeInvalid, "the %s edge opens no tunnel", kind)
	}
	record := func(event string) { m.tunnelEvents.opened = append(m.tunnelEvents.opened, event) }
	return &edge.TunnelHooks{
		Ensure: func(_ context.Context, name string) (edge.Tunnel, error) {
			record("ensure " + name)
			if m.tunnelEvents.held == nil {
				m.tunnelEvents.held = map[string]string{}
			}
			if _, held := m.tunnelEvents.held[name]; !held {
				m.tunnelEvents.held[name] = fmt.Sprintf("uuid-%d", len(m.tunnelEvents.held)+1)
			}
			id := m.tunnelEvents.held[name]
			if m.tunnelEvents.duringEnsure != nil {
				m.tunnelEvents.duringEnsure()
			}
			return edge.Tunnel{ID: id, Address: id + ".cfargotunnel.com"}, nil
		},
		Configure: func(_ context.Context, id, service string) error {
			if m.tunnelEvents.refusesConfigure != nil {
				return m.tunnelEvents.refusesConfigure
			}
			record("configure " + id + " " + service)
			return nil
		},
		ReadToken: func(_ context.Context, id string) (string, error) {
			record("token " + id)
			return "token-" + id, nil
		},
		Delete: func(_ context.Context, id string) error {
			if m.tunnelEvents.refusesDelete != nil {
				return m.tunnelEvents.refusesDelete
			}
			record("delete " + id)
			return nil
		},
	}, nil
}

func (m *machine) ReserveTunnel(_ context.Context, front edge.Kind) (host.Tunnel, error) {
	if err := m.refuse("ReserveTunnel"); err != nil {
		return host.Tunnel{}, err
	}
	if m.tunnel == nil {
		m.tunnel = &host.Tunnel{Edge: front, Name: fmt.Sprintf("ocel-box-%d", m.tunnelsReserved)}
		m.tunnelsReserved++
	}
	if m.tunnel.Edge != front {
		return host.Tunnel{}, refusal.Refuse(refusal.CodeBusy, "the box is reached through the %s edge's tunnel", m.tunnel.Edge)
	}
	return *m.tunnel, nil
}

func (m *machine) RunTunnel(ctx context.Context, tunnel host.Tunnel, token func(context.Context) (string, error)) error {
	if m.tunnel == nil || m.tunnel.Name != tunnel.Name {
		return refusal.Refuse(refusal.CodeBusy, "the tunnel %s no longer reaches the box", tunnel.Name)
	}
	m.tunnel = &tunnel
	if m.tunnelRunning == tunnel.ID {
		return nil
	}
	read, err := token(ctx)
	if err != nil {
		return err
	}
	m.calls = append(m.calls, "run tunnel "+tunnel.ID+" with "+read)
	m.tunnelRunning = tunnel.ID
	return nil
}

func (m *machine) TunnelHost(_ context.Context, hostname, owner, name string) error {
	if m.tunnel == nil || m.tunnel.Name != name {
		return refusal.Refuse(refusal.CodeBusy, "the tunnel %s no longer reaches the box", name)
	}
	m.calls = append(m.calls, "tunnel "+hostname)
	m.tunneled = append(slices.DeleteFunc(m.tunneled, func(held host.TunneledHost) bool { return held.Hostname == hostname }),
		host.TunneledHost{Hostname: hostname, Owner: owner})
	return nil
}

func (m *machine) UntunnelHost(_ context.Context, hostname, owner string) error {
	m.tunneled = slices.DeleteFunc(m.tunneled, func(held host.TunneledHost) bool { return held.Hostname == hostname && held.Owner == owner })
	return nil
}

func (m *machine) ReleaseTunnel(context.Context) ([]host.Tunnel, error) {
	if m.tunnel != nil && len(m.tunneled) == 0 {
		m.retired = append(m.retired, *m.tunnel)
		m.calls = append(m.calls, "stop tunnel "+m.tunnel.ID)
		m.tunnel, m.tunnelRunning = nil, ""
	}
	return slices.Clone(m.retired), nil
}

func (m *machine) ForgetTunnel(_ context.Context, name string) error {
	m.retired = slices.DeleteFunc(m.retired, func(held host.Tunnel) bool { return held.Name == name })
	return nil
}

func tunneledClaim(hostname string) router.Claim {
	return router.Claim{Hostname: hostname, App: "web", Tunnel: tunnelEdge}
}

func TestAClaimThroughATunnelOpensTheBoxsTunnelAndNamesItAsTheOrigin(t *testing.T) {
	m, routed := routedOn(t)

	origin, err := routed.Claim(context.Background(), tunneledClaim("shop.example.com"))
	if err != nil {
		t.Fatalf("Claim through a tunnel: %v", err)
	}

	want := edge.Origin{Address: "uuid-1.cfargotunnel.com", Certified: true, Tunneled: true}
	if origin != want {
		t.Errorf("Claim named origin %+v, want %+v: Cloudflare forwards the hostname to the tunnel, and the tunnel only reaches the box", origin, want)
	}
	if !slices.Equal(m.tunnelEvents.opened, []string{"ensure ocel-box-0", "configure uuid-1 " + host.TunnelService, "token uuid-1"}) {
		t.Errorf("the edge saw %v, want the tunnel opened under the name the box reserved, every hostname sent to the switchboard's tunnel listener, and its token read to run it", m.tunnelEvents.opened)
	}
	surface := box.Surface(slug, environment.TierProduction)
	if !slices.Contains(m.tunneled, host.TunneledHost{Hostname: "shop.example.com", Owner: surface}) {
		t.Errorf("the box tunnels %+v, want shop.example.com for %s", m.tunneled, surface)
	}
	tunneled, claimed := slices.Index(m.calls, "tunnel shop.example.com"), slices.Index(m.calls, "claim shop.example.com")
	if tunneled < 0 || claimed < 0 || tunneled > claimed {
		t.Errorf("calls = %v, want the hostname tunneled before it is claimed: a claimed hostname nothing tunnels is answered by the proxy on the box", m.calls)
	}
	if len(m.shields) != 0 || slices.Contains(m.visited, "RefuseUnshielded") {
		t.Errorf("the box shields %+v, want none: nothing but the tunnel reaches a tunneled hostname", m.shields)
	}
}

func TestEveryClaimThroughTheTunnelSharesTheOneTunnelTheBoxRuns(t *testing.T) {
	m, routed := routedOn(t)
	other := reconciledOn(t, m, "blog")

	if _, err := routed.Claim(context.Background(), tunneledClaim("shop.example.com")); err != nil {
		t.Fatal(err)
	}
	origin, err := other.Claim(context.Background(), tunneledClaim("blog.example.com"))
	if err != nil {
		t.Fatal(err)
	}

	if origin.Address != "uuid-1.cfargotunnel.com" {
		t.Errorf("the second project's claim named %+v, want the tunnel the first opened", origin)
	}
	if configured := slices.DeleteFunc(slices.Clone(m.tunnelEvents.opened), func(event string) bool {
		return !strings.HasPrefix(event, "configure ") && !strings.HasPrefix(event, "token ")
	}); len(configured) != 2 {
		t.Errorf("the edge saw %v, want the tunnel configured and its token read once: the tunnel already running carries every hostname", m.tunnelEvents.opened)
	}
}

func TestTheTunnelIsDeletedWhenTheLastHostnameThroughItIsDisclaimed(t *testing.T) {
	m, routed := routedOn(t)
	ctx := context.Background()
	for _, hostname := range []string{"shop.example.com", "www.shop.example.com"} {
		if _, err := routed.Claim(ctx, tunneledClaim(hostname)); err != nil {
			t.Fatal(err)
		}
	}

	if err := routed.Disclaim(ctx, "shop.example.com"); err != nil {
		t.Fatalf("Disclaim: %v", err)
	}
	if slices.Contains(m.tunnelEvents.opened, "delete uuid-1") {
		t.Fatalf("the tunnel was deleted while www.shop.example.com still goes through it")
	}
	if err := routed.Disclaim(ctx, "www.shop.example.com"); err != nil {
		t.Fatalf("Disclaim: %v", err)
	}

	stopped, deleted := slices.Index(m.calls, "stop tunnel uuid-1"), slices.Index(m.tunnelEvents.opened, "delete uuid-1")
	if stopped < 0 || deleted < 0 {
		t.Errorf("calls = %v and the edge saw %v, want the tunnel stopped on the box and deleted at the edge once nothing goes through it", m.calls, m.tunnelEvents.opened)
	}
}

func TestDestroyingTheLastProjectThroughTheTunnelDeletesIt(t *testing.T) {
	m, routed := routedOn(t)
	if _, err := routed.Claim(context.Background(), tunneledClaim("shop.example.com")); err != nil {
		t.Fatal(err)
	}

	if err := routed.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	if !slices.Contains(m.tunnelEvents.opened, "delete uuid-1") || m.tunnel != nil {
		t.Errorf("the edge saw %v and the box records %+v, want the tunnel deleted with the last project through it", m.tunnelEvents.opened, m.tunnel)
	}
}

func TestAHostnameClaimedAgainWithoutTheTunnelStopsGoingThroughIt(t *testing.T) {
	m, routed := routedOn(t)
	if _, err := routed.Claim(context.Background(), tunneledClaim("shop.example.com")); err != nil {
		t.Fatal(err)
	}

	origin, err := routed.Claim(context.Background(), router.Claim{Hostname: "shop.example.com", App: "web", ClientCertificates: []string{pulled}})
	if err != nil {
		t.Fatalf("Claim without the tunnel: %v", err)
	}

	if origin.Tunneled || origin.Address != address {
		t.Errorf("Claim named %+v, want the box's own address", origin)
	}
	if len(m.tunneled) != 0 || !slices.Contains(m.tunnelEvents.opened, "delete uuid-1") {
		t.Errorf("the box still tunnels %+v, and the edge saw %v, want the hostname off the tunnel and the tunnel deleted: nothing else goes through it", m.tunneled, m.tunnelEvents.opened)
	}
}

func TestThePreviewEntryThroughATunnelIsTunneledOnTheBoxAndReleasedWithIt(t *testing.T) {
	m, front, _ := reconciled(t)
	previews := box.NewRouter(front.Edge)
	ctx := context.Background()

	origin, err := previews.Hooks().Origin.ClaimPreviewEntry(ctx, router.Claim{Hostname: "*.preview.example.com", Tunnel: tunnelEdge})
	if err != nil {
		t.Fatalf("ClaimPreviewEntry through a tunnel: %v", err)
	}
	if !origin.Tunneled || origin.Address != "uuid-1.cfargotunnel.com" {
		t.Errorf("ClaimPreviewEntry named %+v, want the tunnel", origin)
	}
	if m.previewBase != "preview.example.com" || !slices.Contains(m.tunneled, host.TunneledHost{Hostname: "*.preview.example.com", Owner: edge.PreviewEntryOwner}) {
		t.Errorf("the box answers previews on %q and tunnels %+v, want every preview under the wildcard reached through the tunnel", m.previewBase, m.tunneled)
	}

	if err := previews.Hooks().Origin.DisclaimPreviewEntry(ctx, "preview.example.com"); err != nil {
		t.Fatalf("DisclaimPreviewEntry: %v", err)
	}
	if len(m.tunneled) != 0 || !slices.Contains(m.tunnelEvents.opened, "delete uuid-1") {
		t.Errorf("the box tunnels %+v and the edge saw %v, want the wildcard off the tunnel and the tunnel deleted", m.tunneled, m.tunnelEvents.opened)
	}
}
