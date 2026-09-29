package caddyfile_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddyfile"
)

func TestACaddyContainerOffTheHostsNetworkIsRefusedWithoutANetworkNamingIt(t *testing.T) {
	t.Parallel()

	machine := &box{said: map[string]string{"NetworkMode": "bridge\n", caddyfile.AdminServers: "{}"}}
	err := (caddyfile.Caddyfile{Box: machine, Container: "caddy", Port: 8480}).RefuseUnreachable(context.Background())
	if err == nil || !strings.Contains(err.Error(), "proxy.caddy.network") || !strings.Contains(err.Error(), "bridge") {
		t.Errorf("RefuseUnreachable() = %v, want it refused naming proxy.caddy.network: 127.0.0.1:8480 inside a bridged container is its own loopback", err)
	}
}

func TestACaddyContainerOnTheHostsNetworkReachesTheLoopbackPort(t *testing.T) {
	t.Parallel()

	machine := &box{said: map[string]string{"NetworkMode": "host\n", caddyfile.AdminServers: "{}"}}
	if err := (caddyfile.Caddyfile{Box: machine, Container: "caddy", Port: 8480}).RefuseUnreachable(context.Background()); err != nil {
		t.Errorf("RefuseUnreachable() = %v, want a Caddy in the host's network namespace let through", err)
	}
}

func TestACaddyContainerOnANetworkIsNotAskedForItsNetworkMode(t *testing.T) {
	t.Parallel()

	machine := &box{said: map[string]string{"NetworkMode": "bridge\n", caddyfile.AdminServers: "{}"}}
	if err := (caddyfile.Caddyfile{Box: machine, Container: "caddy", Network: "web"}).RefuseUnreachable(context.Background()); err != nil {
		t.Errorf("RefuseUnreachable() = %v, want a Caddy reaching the switchboard by name on web let through", err)
	}
}

func TestACaddyWithItsAdminEndpointOffIsRefused(t *testing.T) {
	t.Parallel()

	machine := &box{refused: map[string]string{caddyfile.AdminServers: "curl: (7) Failed to connect to 127.0.0.1 port 2019"}}
	err := (caddyfile.Caddyfile{Box: machine, Port: 8480}).RefuseUnreachable(context.Background())
	if err == nil || !strings.Contains(err.Error(), "admin off") {
		t.Errorf("RefuseUnreachable() = %v, want it refused naming admin off: caddy reload needs the admin endpoint", err)
	}
}
