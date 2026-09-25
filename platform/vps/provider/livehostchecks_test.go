package vps_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

const adminDecoy = "ocel-live-admin-decoy"

func hostChecksOn(t *testing.T, p *vps.Provider, hostnames []string) []providerkit.HostCheck {
	t.Helper()
	checks, err := p.CheckHost(context.Background(), providerkit.HostCheckRequest{
		Class:     providerkit.ClassProduction,
		Hostnames: hostnames,
	})
	if err != nil {
		t.Fatalf("CheckHost() = %v", err)
	}
	if len(checks) == 0 {
		t.Fatal("CheckHost() answered nothing, so there is no window a verdict can be read out of")
	}
	return checks
}

func about(t *testing.T, checks []providerkit.HostCheck, subject string) providerkit.HostCheck {
	t.Helper()
	for _, check := range checks {
		if strings.Contains(check.Subject, subject) {
			return check
		}
	}
	t.Fatalf("CheckHost() answered %+v, and none of it is about %q", checks, subject)
	return providerkit.HostCheck{}
}

func TestLiveTheHostCheckVerdictsReadOffABootstrappedBoxAndGateNothing(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	owed := "ocel-live-host-checks.invalid"
	checks := hostChecksOn(t, p, []string{owed, "*.preview." + owed})

	dns := about(t, checks, owed)
	if dns.Verdict != providerkit.HostOwed {
		t.Errorf("the verdict for %s is %v (%q), want it owed: a name nothing resolves is a record a human has not written yet", owed, dns.Verdict, dns.Finding)
	}

	reach := about(t, checks, ":"+caddy.HTTPPort)
	if reach.Verdict != providerkit.HostPass {
		t.Errorf("port %s on this box = %v (%q), want a connection from here to succeed: the proxy renews every certificate on it over http-01", caddy.HTTPPort, reach.Verdict, reach.Finding)
	}

	admin := about(t, checks, adminPort)
	if admin.Verdict != providerkit.HostPass {
		t.Errorf("tcp %s inside %s = %v (%q), want nothing bound: the stock admin api hands every container on the shared network arbitrary config replacement",
			adminPort, caddy.Container, admin.Verdict, admin.Finding)
	}

	for _, check := range checks {
		if check.Verdict == providerkit.HostFail {
			t.Errorf("a bootstrapped box whose only owed thing is a dns record failed %q: %s", check.Subject, check.Finding)
		}
	}
	if !vm.running(t, caddy.Container) {
		t.Fatalf("%s is not running, so the verdicts above were read off a box that was never standing", caddy.Container)
	}
}

func TestLiveTheAdminPortBoundInsideTheProxyFailsTheHostCheckVerdict(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	before := about(t, hostChecksOn(t, p, nil), adminPort)
	if before.Verdict != providerkit.HostPass {
		t.Fatalf("tcp %s inside %s is already %v (%q), so this test cannot tell what it induced from what it found",
			adminPort, caddy.Container, before.Verdict, before.Finding)
	}

	vm.ssh(t, "sudo docker rm -f "+adminDecoy+" >/dev/null 2>&1 || true")
	vm.ssh(t, "sudo docker run -d --name "+adminDecoy+
		" --network container:"+caddy.Container+
		" -e PORT="+adminPort+" "+fixtureAt("one"))
	defer vm.ssh(t, "sudo docker rm -f "+adminDecoy+" >/dev/null 2>&1 || true")
	if !vm.running(t, adminDecoy) {
		t.Fatalf("%s never came up, so nothing is listening on tcp %s and there is no regression to catch", adminDecoy, adminPort)
	}

	after := about(t, hostChecksOn(t, p, nil), adminPort)
	if after.Verdict != providerkit.HostFail {
		t.Fatalf("tcp %s inside %s reads %v (%q) with a listener deliberately bound on it",
			adminPort, caddy.Container, after.Verdict, after.Finding)
	}
	if !strings.Contains(after.Finding, adminPort) {
		t.Errorf("the finding is %q, want the port named", after.Finding)
	}
}
