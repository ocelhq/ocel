//go:build integration

package host

import (
	"net/http"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

func TestARealProxyRedirectsAShieldedHostnameOverPlainHTTPToHTTPSAndStillServesTheRest(t *testing.T) {
	certificate, _ := pulledCertificate(t)
	state := twoProjects()
	state.Claims = []boxapi.Claim{
		{Hostname: claimed, Owner: surface, Pointer: pointed},
		{Hostname: "blog.example.com", Owner: otherSurface, Pointer: pointed},
	}
	state.Shields = []boxapi.Shield{{Hostname: claimed, Owner: surface, ClientCAs: []string{certificate}}}
	ask := probing(t, state)

	if said := ask(claimed); said.status != http.StatusPermanentRedirect || said.location != "https://"+claimed+"/" || said.router == boxapi.SwitchboardRouter {
		t.Errorf("a shielded hostname over plain http answered %d to %q from %q, want 308 to https://%s/ before the switchboard: :80 carries no client certificate, so a request that skips the edge must reach no app, and a visitor Cloudflare forwarded over http lands on https", said.status, said.location, said.router, claimed)
	}
	if said := ask("blog.example.com"); said.router != boxapi.SwitchboardRouter {
		t.Errorf("a hostname nothing shields over plain http answered %d from %q, want the switchboard: its http-01 challenge and plain-http leg go through here", said.status, said.router)
	}
}
