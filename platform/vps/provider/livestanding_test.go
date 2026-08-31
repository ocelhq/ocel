package vps_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const adminDecoy = "ocel-live-admin-decoy"

func standingOn(t *testing.T, p *vps.Provider, hostnames []string) []providerkit.StandingCheck {
	t.Helper()
	checks, err := p.CheckStanding(context.Background(), providerkit.StandingRequest{
		Class:     providerkit.ClassProduction,
		Hostnames: hostnames,
	})
	if err != nil {
		t.Fatalf("CheckStanding() = %v", err)
	}
	if len(checks) == 0 {
		t.Fatal("CheckStanding() answered nothing, so there is no window a verdict can be read out of")
	}
	return checks
}

func about(t *testing.T, checks []providerkit.StandingCheck, subject string) providerkit.StandingCheck {
	t.Helper()
	for _, check := range checks {
		if strings.Contains(check.Subject, subject) {
			return check
		}
	}
	t.Fatalf("CheckStanding() answered %+v, and none of it is about %q", checks, subject)
	return providerkit.StandingCheck{}
}

func TestLiveTheStandingVerdictsReadOffABootstrappedBoxAndGateNothing(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	owed := "ocel-live-standing.invalid"
	checks := standingOn(t, p, []string{owed, "*.preview." + owed})

	dns := about(t, checks, owed)
	if dns.Verdict != providerkit.StandingOwed {
		t.Errorf("the verdict for %s is %v (%q), want it owed: a name nothing resolves is a record a human has not written yet", owed, dns.Verdict, dns.Finding)
	}

	reach := about(t, checks, ":"+host.RenewalPort)
	if reach.Verdict != providerkit.StandingPass {
		t.Errorf("port %s on this box = %v (%q), want a connection from here to succeed: the proxy renews every certificate on it over http-01", host.RenewalPort, reach.Verdict, reach.Finding)
	}

	admin := about(t, checks, host.AdminPort)
	if admin.Verdict != providerkit.StandingPass {
		t.Errorf("tcp %s inside %s = %v (%q), want nothing bound: the stock admin api hands every container on the shared network arbitrary config replacement",
			host.AdminPort, host.ProxyContainer, admin.Verdict, admin.Finding)
	}

	for _, check := range checks {
		if check.Verdict == providerkit.StandingFail {
			t.Errorf("a bootstrapped box whose only owed thing is a dns record failed %q: %s", check.Subject, check.Finding)
		}
	}
	if !vm.running(t, host.ProxyContainer) {
		t.Fatalf("%s is not running, so the verdicts above were read off a box that was never standing", host.ProxyContainer)
	}
}

func TestLiveTheAdminPortBoundInsideTheProxyFailsTheStandingVerdict(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	before := about(t, standingOn(t, p, nil), host.AdminPort)
	if before.Verdict != providerkit.StandingPass {
		t.Fatalf("tcp %s inside %s is already %v (%q), so this test cannot tell what it induced from what it found",
			host.AdminPort, host.ProxyContainer, before.Verdict, before.Finding)
	}

	vm.ssh(t, "sudo docker rm -f "+adminDecoy+" >/dev/null 2>&1 || true")
	vm.ssh(t, "sudo docker run -d --name "+adminDecoy+
		" --network container:"+host.ProxyContainer+
		" -e PORT="+host.AdminPort+" "+fixtureAt("one"))
	defer vm.ssh(t, "sudo docker rm -f "+adminDecoy+" >/dev/null 2>&1 || true")
	if !vm.running(t, adminDecoy) {
		t.Fatalf("%s never came up, so nothing is listening on tcp %s and there is no regression to catch", adminDecoy, host.AdminPort)
	}

	after := about(t, standingOn(t, p, nil), host.AdminPort)
	if after.Verdict != providerkit.StandingFail {
		t.Fatalf("tcp %s inside %s reads %v (%q) with a listener deliberately bound on it",
			host.AdminPort, host.ProxyContainer, after.Verdict, after.Finding)
	}
	if !strings.Contains(after.Finding, host.AdminPort) {
		t.Errorf("the finding is %q, want the port named", after.Finding)
	}
}
