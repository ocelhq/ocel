package vps_test

import (
	"context"
	"strings"
	"testing"

	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func overABox(t *testing.T, machine *box) *vps.Provider {
	t.Helper()

	return vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
}

func TestForgettingAPreviewsCertificatesLeavesThemForThePermissionCheckToRetire(t *testing.T) {
	t.Parallel()

	machine := &box{}
	spoken := &said{}
	hostnames := []string{"shop--pr-7--api.preview.example.com", "shop--pr-7--web.preview.example.com"}

	if err := overABox(t, machine).Host().ForgetCertificates(context.Background(), hostnames, spoken); err != nil {
		t.Fatalf("ForgetCertificates = %v", err)
	}
	for _, command := range machine.commands() {
		if strings.Contains(command, "/caddy/certificates") {
			t.Errorf("the teardown reached the proxy's store with %q: caddy keeps the pair in memory after its storage is gone and renews one it cannot find in storage without asking the switchboard, so a pair taken here is ordered again for a name nothing claims", command)
		}
	}
	if len(spoken.lines) != 0 {
		t.Errorf("the teardown reported %v as taken off the box, and nothing was", spoken.lines)
	}
}
