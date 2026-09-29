package caddyfile_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddyfile"
)

func coolifys(t *testing.T) (*box, caddyfile.Caddyfile) {
	t.Helper()
	machine := &box{said: map[string]string{caddyfile.AdminServers: golden(t, "coolify.json")}}
	return machine, caddyfile.Caddyfile{Box: machine, Preset: "coolify", Directory: "/data/coolify/proxy/caddy/dynamic", Container: "coolify-proxy", Config: "/config/caddy/Caddyfile.autosave", Network: "coolify"}
}

func TestAHostnameASiteOfYoursServesIsRefusedNamingTheServerAndTheRoute(t *testing.T) {
	t.Parallel()

	_, front := coolifys(t)
	err := front.RefuseRouted(context.Background(), []string{"new.p1305.test", "Web.p1305.test"})
	if err == nil {
		t.Fatal("RefuseRouted(web.p1305.test) = nil, want it refused: ocel's block would shadow the site you serve it with")
	}
	for _, want := range []string{"Web.p1305.test", "srv0", "route 3", "web.p1305.test"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("RefuseRouted() = %v, want it naming %q", err, want)
		}
	}
}

func TestAHostnameAWildcardSiteOfYoursCoversIsRefused(t *testing.T) {
	t.Parallel()

	_, front := coolifys(t)
	err := front.RefuseRouted(context.Background(), []string{"shop.apps.p1305.test"})
	if err == nil || !strings.Contains(err.Error(), "*.apps.p1305.test") {
		t.Errorf("RefuseRouted(shop.apps.p1305.test) = %v, want it refused naming the wildcard *.apps.p1305.test that covers it", err)
	}
}

func TestAHostMatcherInsideASiteOfYoursIsReadToo(t *testing.T) {
	t.Parallel()

	machine := &box{said: map[string]string{caddyfile.AdminServers: `{"srv0":{"listen":[":443"],"routes":[{"handle":[{"handler":"subroute","routes":[{"match":[{"host":["api.example.com"]}],"handle":[{"handler":"reverse_proxy","upstreams":[{"dial":"10.0.0.2:80"}]}]}]}]}]}}`}}
	err := (caddyfile.Caddyfile{Box: machine, Port: 8480}).RefuseRouted(context.Background(), []string{"api.example.com"})
	if err == nil || !strings.Contains(err.Error(), "api.example.com") {
		t.Errorf("RefuseRouted(api.example.com) = %v, want it refused: a host matcher nested in a route of yours routes it all the same", err)
	}
}

func TestTheHostnamesOcelsOwnBlockServesAndOnesNothingServesAreFree(t *testing.T) {
	t.Parallel()

	_, front := coolifys(t)
	if err := front.RefuseRouted(context.Background(), []string{"shop.p1305.test", "ocel-edge-probe.preview.p1305.test", "blog.p1305.test", "p1305.test", "a.b.apps.p1305.test"}); err != nil {
		t.Errorf("RefuseRouted() = %v, want nothing refused: ocel's own block is no site of yours, a catch-all with no host names no hostname, and a wildcard covers one label", err)
	}
}

func TestTheCollisionReadAsksTheAdminEndpointInsideYourContainer(t *testing.T) {
	t.Parallel()

	machine, front := coolifys(t)
	if err := front.RefuseRouted(context.Background(), []string{"blog.p1305.test"}); err != nil {
		t.Fatalf("RefuseRouted() = %v", err)
	}
	want := [][]string{{"docker", "exec", "coolify-proxy", "wget", "-qO-", caddyfile.AdminServers}}
	if got := machine.argvs(); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("RefuseRouted() ran %q, want %q", got, want)
	}
}

func TestTheCollisionReadAsksTheAdminEndpointOnTheHostsLoopbackForACaddyService(t *testing.T) {
	t.Parallel()

	machine := &box{said: map[string]string{caddyfile.AdminServers: "null\n"}}
	if err := (caddyfile.Caddyfile{Box: machine, Port: 8480}).RefuseRouted(context.Background(), []string{"blog.example.com"}); err != nil {
		t.Fatalf("RefuseRouted() over a Caddy serving no http app = %v, want nothing refused", err)
	}
	want := [][]string{{"curl", "-fsS", caddyfile.AdminServers}}
	if got := machine.argvs(); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("RefuseRouted() ran %q, want %q", got, want)
	}
}

func TestAnAdminEndpointThatDoesNotAnswerRefusesTheClaimNamingAdminOff(t *testing.T) {
	t.Parallel()

	machine := &box{refused: map[string]string{caddyfile.AdminServers: "curl: (7) Failed to connect to 127.0.0.1 port 2019"}}
	err := (caddyfile.Caddyfile{Box: machine, Port: 8480}).RefuseRouted(context.Background(), []string{"blog.example.com"})
	if err == nil || !strings.Contains(err.Error(), "admin off") || !strings.Contains(err.Error(), "port 2019") {
		t.Errorf("RefuseRouted() = %v, want it refused naming admin off and what the read met: ocel cannot tell what your Caddy serves without it", err)
	}
}
