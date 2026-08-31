package vps_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const boxAddress = "203.0.113.10"

func stubResolver(answers map[string][]string) vps.Lookup {
	return func(_ context.Context, hostname string) ([]netip.Addr, error) {
		held, known := answers[hostname]
		if !known {
			return nil, &net.DNSError{Err: "no such host", Name: hostname, IsNotFound: true}
		}
		addrs := make([]netip.Addr, 0, len(held))
		for _, written := range held {
			addrs = append(addrs, netip.MustParseAddr(written))
		}
		return addrs, nil
	}
}

func hereOnly() map[string][]string {
	return map[string][]string{boxAddress: {boxAddress}}
}

func TestAHostnameThatResolvesToThisBoxPasses(t *testing.T) {
	t.Parallel()

	answers := hereOnly()
	answers["shop.example.com"] = []string{boxAddress}
	check := vps.DNSVerdict(context.Background(), stubResolver(answers), "shop.example.com", boxAddress)

	if check.Verdict != providerkit.StandingPass {
		t.Fatalf("verdict = %v (%q), want a pass", check.Verdict, check.Finding)
	}
	if !strings.Contains(check.Finding, boxAddress) {
		t.Errorf("finding = %q, want the address it resolved to named", check.Finding)
	}
	if check.Fix != "" {
		t.Errorf("a hostname that already points here carries the fix %q", check.Fix)
	}
}

func TestAHostnameThatResolvesSomewhereElseNamesBothAnswers(t *testing.T) {
	t.Parallel()

	answers := hereOnly()
	answers["shop.example.com"] = []string{"198.51.100.7"}
	check := vps.DNSVerdict(context.Background(), stubResolver(answers), "shop.example.com", boxAddress)

	if check.Verdict != providerkit.StandingFail {
		t.Fatalf("verdict = %v (%q), want a failure", check.Verdict, check.Finding)
	}
	if !strings.Contains(check.Finding, "198.51.100.7") {
		t.Errorf("finding = %q, want the wrong answer named: no surveyed tool tells an operator where their hostname actually points", check.Finding)
	}
	if !strings.Contains(check.Finding, boxAddress) {
		t.Errorf("finding = %q, want this box's own address named beside the wrong answer", check.Finding)
	}
	if !strings.Contains(check.Fix, boxAddress) {
		t.Errorf("fix = %q, want the address to point the record at", check.Fix)
	}
}

func TestAHostnameThatDoesNotResolveIsOwedRatherThanBroken(t *testing.T) {
	t.Parallel()

	check := vps.DNSVerdict(context.Background(), stubResolver(hereOnly()), "shop.example.com", boxAddress)

	if check.Verdict != providerkit.StandingOwed {
		t.Fatalf("verdict = %v (%q), want it owed: instructions-only dns makes records-owed the state between `ocel domain add` and a human editing their zone", check.Verdict, check.Finding)
	}
	if !strings.Contains(check.Finding, "owed") {
		t.Errorf("finding = %q, want it named as owed rather than as broken", check.Finding)
	}
	if check.Fix == "" {
		t.Error("a hostname whose record is owed carries no fix, and the fix is the whole point of saying so")
	}
}

func TestAResolverThatFellOverIsNotReadAsARecordNobodyWrote(t *testing.T) {
	t.Parallel()

	fell := func(context.Context, string) ([]netip.Addr, error) {
		return nil, &net.DNSError{Err: "server misbehaving", Name: "shop.example.com", IsTemporary: true}
	}
	answers := hereOnly()
	look := func(ctx context.Context, hostname string) ([]netip.Addr, error) {
		if hostname == boxAddress {
			return stubResolver(answers)(ctx, hostname)
		}
		return fell(ctx, hostname)
	}
	check := vps.DNSVerdict(context.Background(), look, "shop.example.com", boxAddress)

	if check.Verdict == providerkit.StandingOwed {
		t.Fatalf("a resolver that fell over was read as a record nobody wrote: %q", check.Finding)
	}
	if !strings.Contains(check.Finding, "server misbehaving") {
		t.Errorf("finding = %q, want what the resolver said", check.Finding)
	}
}

func TestAPreviewWildcardIsAskedAboutUnderTheNameThatResolves(t *testing.T) {
	t.Parallel()

	answers := hereOnly()
	answers["ocel-edge-probe.preview.example.com"] = []string{boxAddress}
	checks := vps.DNSVerdicts(context.Background(), stubResolver(answers),
		[]string{"*.preview.example.com", "*.preview.example.com"}, boxAddress)

	if len(checks) != 1 {
		t.Fatalf("got %d verdicts over one wildcard named twice, want one", len(checks))
	}
	if checks[0].Subject != "ocel-edge-probe.preview.example.com" {
		t.Errorf("subject = %q, want the probe hostname a wildcard is asked about under", checks[0].Subject)
	}
	if checks[0].Verdict != providerkit.StandingPass {
		t.Errorf("verdict = %v (%q), want the pass the stub answers with", checks[0].Verdict, checks[0].Finding)
	}
}

func TestTheReachVerdictClaimsOnePathInAndNeverTheInternet(t *testing.T) {
	t.Parallel()

	check := vps.ReachVerdict(context.Background(), func(context.Context, string) error { return nil }, boxAddress)
	if check.Verdict != providerkit.StandingPass {
		t.Fatalf("verdict = %v (%q), want a pass where the dial succeeded", check.Verdict, check.Finding)
	}
	if check.Finding == "" {
		t.Fatal("the passing reach verdict says nothing, so there is no wording to hold to what it can prove")
	}
	if strings.Contains(check.Finding, "reachable") {
		t.Errorf("finding = %q, and a check that says reachable and means reachable from here would be cited when a certificate silently fails to renew", check.Finding)
	}
	if !strings.Contains(check.Finding, "one path in") {
		t.Errorf("finding = %q, want exactly what a dial from here demonstrates", check.Finding)
	}
}

func TestTheReachVerdictFailsWhenNothingAnsweredAndNamesTheFirewall(t *testing.T) {
	t.Parallel()

	check := vps.ReachVerdict(context.Background(),
		func(context.Context, string) error { return errors.New("connection refused") }, boxAddress)
	if check.Verdict != providerkit.StandingFail {
		t.Fatalf("verdict = %v (%q), want a failure where nothing answered", check.Verdict, check.Finding)
	}
	if !strings.Contains(check.Finding, "http-01") {
		t.Errorf("finding = %q, want the renewal that needs the port named: nothing resident belonging to ocel notices a firewall that closed", check.Finding)
	}
	if !strings.Contains(check.Fix, "firewall") {
		t.Errorf("fix = %q, want the firewall named", check.Fix)
	}
}

func standingOver(machine *scripted) []providerkit.StandingCheck {
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
	p.Resolving(stubResolver(map[string][]string{"box.invalid": {boxAddress}}))
	p.Reaching(func(context.Context, string) error { return nil })
	checks, err := p.CheckStanding(context.Background(), providerkit.StandingRequest{
		Class: providerkit.ClassProduction,
	})
	if err != nil {
		panic(err)
	}
	return checks
}

func adminCheck(t *testing.T, checks []providerkit.StandingCheck) providerkit.StandingCheck {
	t.Helper()
	if len(checks) == 0 {
		t.Fatal("CheckStanding() answered nothing at all, so there is no window to read a verdict out of")
	}
	for _, check := range checks {
		if strings.Contains(check.Subject, host.AdminPort) {
			return check
		}
	}
	t.Fatalf("CheckStanding() answered %+v and none of it is about tcp %s inside the proxy", checks, host.AdminPort)
	return providerkit.StandingCheck{}
}

func TestNothingOnTheStockAdminPortInsideTheProxyPasses(t *testing.T) {
	t.Parallel()

	check := adminCheck(t, standingOver(boxSaying(map[string]answer{"'listeners'": {stdout: "0.0.0.0:80\n[::]:443\n"}})))
	if check.Verdict != providerkit.StandingPass {
		t.Fatalf("verdict = %v (%q), want a pass where nothing is bound on %s", check.Verdict, check.Finding, host.AdminPort)
	}
	if !strings.Contains(check.Finding, host.ProxyAdminSocket) {
		t.Errorf("finding = %q, want the socket the admin endpoint is reached over named", check.Finding)
	}
}

func TestTheStockAdminPortBoundInsideTheProxyFails(t *testing.T) {
	t.Parallel()

	check := adminCheck(t, standingOver(boxSaying(map[string]answer{
		"'listeners'": {stdout: "0.0.0.0:80\n0.0.0.0:" + host.AdminPort + "\n"},
	})))
	if check.Verdict != providerkit.StandingFail {
		t.Fatalf("verdict = %v (%q), want a failure where the stock admin api is bound", check.Verdict, check.Finding)
	}
	if !strings.Contains(check.Finding, "0.0.0.0:"+host.AdminPort) {
		t.Errorf("finding = %q, want the bind named", check.Finding)
	}
	if !strings.Contains(check.Finding, host.ProxyNetwork) {
		t.Errorf("finding = %q, want what the exposure reaches named: every container on the shared network", check.Finding)
	}
}

func TestAProxyThatCouldNotBeReadIsNotReadAsACleanNamespace(t *testing.T) {
	t.Parallel()

	check := adminCheck(t, standingOver(boxSaying(map[string]answer{
		"'listeners'": {code: 1, stderr: "Error: No such container: " + host.ProxyContainer},
	})))
	if check.Verdict == providerkit.StandingPass {
		t.Fatalf("a proxy this box could not read passed as one with nothing bound on %s: %q", host.AdminPort, check.Finding)
	}
}

type addressless struct{ *scripted }

func (a addressless) Destination() session.Destination {
	held := a.scripted.Destination()
	held.Address = ""
	return held
}

func TestABoxWhoseOwnAddressCouldNotBeReadReportsAndNeverRefuses(t *testing.T) {
	t.Parallel()

	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return addressless{boxSaying(nil)}, nil },
	)
	checks, err := p.CheckStanding(context.Background(), providerkit.StandingRequest{
		Class:     providerkit.ClassProduction,
		Hostnames: []string{"shop.example.com"},
	})
	if err != nil {
		t.Fatalf("CheckStanding() = %v, and this same rpc runs on every `ocel deploy`: a standing concern is a report and never a gate", err)
	}
	if len(checks) == 0 {
		t.Fatal("CheckStanding() answered nothing at all over a box whose address could not be read, and an empty report is read as a box with nothing wrong")
	}
	for _, check := range checks {
		if check.Verdict != providerkit.StandingFail {
			t.Errorf("check %+v passed over a box whose own address could not be read", check)
		}
	}
	if !strings.Contains(checks[0].Finding, "address") {
		t.Errorf("finding = %q, want the address read that failed named", checks[0].Finding)
	}
}

func TestAProxyThatNamedNoSocketAtAllIsNotReadAsACleanNamespace(t *testing.T) {
	t.Parallel()

	check := adminCheck(t, standingOver(boxSaying(map[string]answer{"'listeners'": {stdout: ""}})))
	if check.Verdict != providerkit.StandingFail {
		t.Fatalf("verdict = %v (%q), want a failure: a running proxy always holds %s and %s, so a namespace naming nothing is one this box never read rather than one with a clean admin port",
			check.Verdict, check.Finding, host.RenewalPort, "443")
	}
	if !strings.Contains(check.Finding, "no listening socket at all") {
		t.Errorf("finding = %q, want it to say the proxy named nothing rather than to report the admin port clean", check.Finding)
	}
	if check.Fix == "" {
		t.Error("a proxy that named no socket carries no fix, and there is nothing for an operator to do with the finding alone")
	}
}
