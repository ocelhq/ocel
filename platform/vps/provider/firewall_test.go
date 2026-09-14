package vps_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	vps "github.com/ocelhq/ocel/platform/vps/provider"
)

func TestTheReachVerdictNeverSendsTheUserToAHostFirewallDockerWritesAround(t *testing.T) {
	t.Parallel()

	check := vps.ReachVerdict(context.Background(),
		func(context.Context, string) error { return errors.New("connection refused") }, boxAddress)
	if strings.Contains(check.Fix, "this machine's firewall") {
		t.Errorf("fix = %q, and the proxy publishes its ports through docker, whose iptables rules sit ahead of ufw and firewalld: opening the port there changes nothing", check.Fix)
	}
	for _, wanted := range []string{"security group", "iptables"} {
		if !strings.Contains(check.Fix, wanted) {
			t.Errorf("fix = %q, want %q named: the firewall that can close this port is the provider's, and the reason a host firewall cannot is docker's own rules", check.Fix, wanted)
		}
	}
}
