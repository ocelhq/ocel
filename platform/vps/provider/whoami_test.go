package vps_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

type surveyed struct {
	facts session.Facts
	err   error
}

func (s surveyed) Facts(context.Context) (session.Facts, error) { return s.facts, s.err }

func (s surveyed) HostKey() provider.HostKey {
	return provider.HostKey{Type: "ssh-ed25519", Fingerprint: "SHA256:whoami"}
}

func (s surveyed) Destination() session.Destination {
	return session.Destination{Written: "box.example", Address: "203.0.113.7", Port: 22, User: "ocel-deploy"}
}

func deployLoginFacts() session.Facts {
	return session.Facts{
		Systemd: true,
		OS:      "Ubuntu 24.04 LTS",
		Arch:    "x86_64",
		Kernel:  "6.8.0",
		Tools:   session.BootstrapTools(),
	}
}

func TestWhoamiAnswersForTheLoginEveryDeployRunsAs(t *testing.T) {
	t.Parallel()

	principal, err := vps.Whoami(context.Background(), surveyed{facts: deployLoginFacts()})
	if err != nil {
		t.Fatalf("Whoami() = %v, want %s as the principal: a bootstrapped host runs every deploy as that login and grants it no passwordless sudo, and a preflight that cannot say who is calling returns before it says anything else — no bootstrap state, no known slugs, and no claim on a hostname another project on the box already serves",
			err, "ocel-deploy")
	}
	if principal.Name != "ocel-deploy" {
		t.Errorf("Whoami().Principal = %q, want the login every deploy runs as", principal.Name)
	}
	if principal.Account != "box.example" {
		t.Errorf("Whoami().Account = %q, want the host as the user wrote it", principal.Account)
	}
	if principal.Location != "" {
		t.Errorf("Whoami().Location = %q, want nothing: a machine is where the user put it", principal.Location)
	}
	if key := detail(principal, "host key"); key != "ssh-ed25519 SHA256:whoami" {
		t.Errorf("Whoami() host key = %q, want the key's type and the fingerprint that was verified", key)
	}
	if addr := detail(principal, "address"); addr != "203.0.113.7 port 22" {
		t.Errorf("Whoami() address = %q, want the address the name resolved to and the port it answered on", addr)
	}
	if elevation := detail(principal, "elevation"); elevation != "neither root nor sudo without a password" {
		t.Errorf("Whoami() names the elevation %q for a login with neither, want it said plainly: a deploy login reported as having passwordless sudo is the one grant `ocel permissions deploy` promises it never has", elevation)
	}
}

func TestWhoamiPassesUpTheHostThatWouldNotAnswer(t *testing.T) {
	t.Parallel()

	_, err := vps.Whoami(context.Background(), surveyed{err: errors.New("connection closed by remote host")})
	if err == nil || !strings.Contains(err.Error(), "connection closed") {
		t.Fatalf("Whoami() = %v, want the machine's own failure to answer passed up rather than a principal invented for it", err)
	}
}

func detail(principal provider.Principal, label string) string {
	for _, entry := range principal.Details {
		if entry.Label == label {
			return entry.Value
		}
	}
	return ""
}
