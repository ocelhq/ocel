package vps_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
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

	if check.Verdict != providerkit.HostPass {
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

	if check.Verdict != providerkit.HostFail {
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

	if check.Verdict != providerkit.HostOwed {
		t.Fatalf("verdict = %v (%q), want it owed: instructions-only dns makes records-owed the state between `ocel domain add` and a human editing their zone", check.Verdict, check.Finding)
	}
	if !strings.Contains(check.Finding, "owed") {
		t.Errorf("finding = %q, want it named as owed rather than as broken", check.Finding)
	}
	if check.Fix == "" {
		t.Error("a hostname whose record is owed carries no fix, and the fix is the whole point of saying so")
	}
}

func TestAHostnameThatResolvesOnlyToLoopbackIsOwedRatherThanPointedElsewhere(t *testing.T) {
	t.Parallel()

	answers := hereOnly()
	answers["shop.localhost"] = []string{"::1", "127.0.0.1"}
	check := vps.DNSVerdict(context.Background(), stubResolver(answers), "shop.localhost", boxAddress)

	if check.Verdict != providerkit.HostOwed {
		t.Fatalf("verdict = %v (%q), want it owed: a loopback answer is the one every machine gives for itself, so it points nowhere else and no zone holds a record to correct", check.Verdict, check.Finding)
	}
	for _, want := range []string{"127.0.0.1", "::1", boxAddress, "owed"} {
		if !strings.Contains(check.Finding, want) {
			t.Errorf("finding = %q, want %q named", check.Finding, want)
		}
	}
	if check.Fix == "" {
		t.Error("a hostname whose record is owed carries no fix, and the fix is the whole point of saying so")
	}
}

func TestAHostnameThatResolvesToLoopbackBesideAnotherMachineIsPointedElsewhere(t *testing.T) {
	t.Parallel()

	answers := hereOnly()
	answers["shop.example.com"] = []string{"127.0.0.1", "198.51.100.7"}
	check := vps.DNSVerdict(context.Background(), stubResolver(answers), "shop.example.com", boxAddress)

	if check.Verdict != providerkit.HostFail {
		t.Fatalf("verdict = %v (%q), want a failure: a record that names another machine is wrong however many loopback answers stand beside it", check.Verdict, check.Finding)
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

	if check.Verdict == providerkit.HostOwed {
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
	if checks[0].Verdict != providerkit.HostPass {
		t.Errorf("verdict = %v (%q), want the pass the stub answers with", checks[0].Verdict, checks[0].Finding)
	}
}

func TestTheReachVerdictClaimsOnePathInAndNeverTheInternet(t *testing.T) {
	t.Parallel()

	check := vps.ReachVerdict(context.Background(), func(context.Context, string) error { return nil }, boxAddress)
	if check.Verdict != providerkit.HostPass {
		t.Fatalf("verdict = %v (%q), want a pass where the dial succeeded", check.Verdict, check.Finding)
	}
	if check.Finding == "" {
		t.Fatal("the passing reach verdict says nothing, so there is no wording to hold to what it can prove")
	}
	if strings.Contains(check.Finding, "reachable") {
		t.Errorf("finding = %q, and a check that says reachable and means reachable from here would be cited when a certificate silently fails to renew", check.Finding)
	}
	if !strings.Contains(check.Finding, "not proof the internet reaches it") {
		t.Errorf("finding = %q, want exactly what a dial from here demonstrates", check.Finding)
	}
}

func TestTheReachVerdictFailsWhenNothingAnsweredAndNamesTheFirewall(t *testing.T) {
	t.Parallel()

	check := vps.ReachVerdict(context.Background(),
		func(context.Context, string) error { return errors.New("connection refused") }, boxAddress)
	if check.Verdict != providerkit.HostFail {
		t.Fatalf("verdict = %v (%q), want a failure where nothing answered", check.Verdict, check.Finding)
	}
	if !strings.Contains(check.Finding, "http-01") {
		t.Errorf("finding = %q, want the renewal that needs the port named: nothing resident belonging to ocel notices a firewall that closed", check.Finding)
	}
	if !strings.Contains(check.Fix, "firewall") {
		t.Errorf("fix = %q, want the firewall named", check.Fix)
	}
}

func standingOver(machine *scripted) []providerkit.HostCheck {
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
	p.Resolving(stubResolver(map[string][]string{"box.invalid": {boxAddress}}))
	p.Reaching(func(context.Context, string) error { return nil })
	checks, err := p.CheckHost(context.Background(), providerkit.HostCheckRequest{
		Class: providerkit.ClassProduction,
	})
	if err != nil {
		panic(err)
	}
	return checks
}

func socketTable(ports ...int) string {
	written := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	for at, port := range ports {
		written += fmt.Sprintf("   %d: 00000000:%04X 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0000000000000000 100 0 0 10 0\n", at, port)
	}
	return written
}

func adminCheck(t *testing.T, checks []providerkit.HostCheck) providerkit.HostCheck {
	t.Helper()
	if len(checks) == 0 {
		t.Fatal("CheckHost() answered nothing at all, so there is no window to read a verdict out of")
	}
	for _, check := range checks {
		if strings.Contains(check.Subject, adminPort) {
			return check
		}
	}
	t.Fatalf("CheckHost() answered %+v and none of it is about tcp %s inside the proxy", checks, adminPort)
	return providerkit.HostCheck{}
}

func TestNothingOnTheStockAdminPortInsideTheProxyPasses(t *testing.T) {
	t.Parallel()

	check := adminCheck(t, standingOver(boxSaying(map[string]answer{"/proc/net/tcp &&": {stdout: socketTable(80, 443)}})))
	if check.Verdict != providerkit.HostPass {
		t.Fatalf("verdict = %v (%q), want a pass where nothing is bound on %s", check.Verdict, check.Finding, adminPort)
	}
	if !strings.Contains(check.Finding, caddy.AdminSocket) {
		t.Errorf("finding = %q, want the socket the admin endpoint is reached over named", check.Finding)
	}
}

func TestTheStockAdminPortBoundInsideTheProxyFails(t *testing.T) {
	t.Parallel()

	check := adminCheck(t, standingOver(boxSaying(map[string]answer{
		"/proc/net/tcp &&": {stdout: socketTable(80, caddy.AdminPort)},
	})))
	if check.Verdict != providerkit.HostFail {
		t.Fatalf("verdict = %v (%q), want a failure where the stock admin api is bound", check.Verdict, check.Finding)
	}
	if !strings.Contains(check.Finding, "0.0.0.0:"+adminPort) {
		t.Errorf("finding = %q, want the bind named", check.Finding)
	}
	if !strings.Contains(check.Finding, "anything that reaches the proxy") {
		t.Errorf("finding = %q, want what the exposure reaches named", check.Finding)
	}
}

func TestAProxyThatCouldNotBeReadIsNotReadAsACleanNamespace(t *testing.T) {
	t.Parallel()

	check := adminCheck(t, standingOver(boxSaying(map[string]answer{
		"/proc/net/tcp &&": {code: 1, stderr: "Error: No such container: " + caddy.Container},
	})))
	if check.Verdict == providerkit.HostPass {
		t.Fatalf("a proxy this box could not read passed as one with nothing bound on %s: %q", adminPort, check.Finding)
	}
}

func boardCheck(t *testing.T, checks []providerkit.HostCheck) providerkit.HostCheck {
	t.Helper()
	for _, check := range checks {
		if check.Subject == host.SwitchboardContainer {
			return check
		}
	}
	t.Fatalf("CheckHost() answered %+v and none of it is about %s, which routes every request the box serves", checks, host.SwitchboardContainer)
	return providerkit.HostCheck{}
}

func TestASwitchboardRunningAndAnsweringOverItsControlSocketPasses(t *testing.T) {
	t.Parallel()

	check := boardCheck(t, standingOver(boxSaying(nil)))
	if check.Verdict != providerkit.HostPass {
		t.Fatalf("verdict = %v (%q), want a pass where the switchboard runs and answers", check.Verdict, check.Finding)
	}
	if !strings.Contains(check.Finding, "control socket") {
		t.Errorf("finding = %q, want what was asked of it named", check.Finding)
	}
}

func TestASwitchboardADeployStoodAgainPassesAndSaysWhenAndWhy(t *testing.T) {
	t.Parallel()

	check := boardCheck(t, standingOver(ahead(boxSaying(nil), "ocel.restored", answer{stdout: "deploy 2026-09-26T23:51:04.18Z\n"})))
	if check.Verdict != providerkit.HostPass {
		t.Fatalf("verdict = %v (%q), want a pass: it runs and answers", check.Verdict, check.Finding)
	}
	for _, wanted := range []string{"control socket", "a deploy stood it again", "2026-09-26T23:51:04.18Z", "prune"} {
		if !strings.Contains(check.Finding, wanted) {
			t.Errorf("finding = %q, want %q in it: a switchboard something removed is worth knowing about even once it is back", check.Finding, wanted)
		}
	}
}

func TestASwitchboardBootstrapStoodSaysNothingOfBeingStoodAgain(t *testing.T) {
	t.Parallel()

	check := boardCheck(t, standingOver(ahead(boxSaying(nil), "ocel.restored", answer{stdout: " 2026-09-01T08:00:00Z\n"})))
	if check.Verdict != providerkit.HostPass || strings.Contains(check.Finding, "again") {
		t.Errorf("check = %v %q, want a plain pass", check.Verdict, check.Finding)
	}
}

func TestASwitchboardThatIsGoneStoppedOrSilentFailsByWhatIsWrongWithIt(t *testing.T) {
	t.Parallel()

	for what, state := range map[string]struct {
		script map[string]answer
		wanted string
	}{
		"not there at all": {map[string]answer{
			"'upstreams'":    {code: 1, stderr: "Error: No such container: " + host.SwitchboardContainer},
			"docker inspect": {code: 1, stdout: "Error: No such object: " + host.SwitchboardContainer},
		}, "no " + host.SwitchboardContainer},
		"exited": {map[string]answer{
			"'upstreams'":    {code: 1, stderr: "Error response from daemon: container is not running"},
			"docker inspect": {stdout: exitedState},
		}, "exited"},
		"silent over its control socket": {map[string]answer{
			"'upstreams'":    {code: 1, stderr: "ocel-switchboard: the switchboard answered nothing over /run/ocel-switchboard/control.sock"},
			"docker inspect": {stdout: runningState},
		}, "answered nothing"},
	} {
		check := boardCheck(t, standingOver(boxSaying(state.script)))
		if check.Verdict != providerkit.HostFail {
			t.Errorf("a switchboard %s = %v (%q), want a failure: nothing the box serves is routed without it", what, check.Verdict, check.Finding)
		}
		if !strings.Contains(check.Finding, state.wanted) {
			t.Errorf("a switchboard %s is found %q, want %q in it", what, check.Finding, state.wanted)
		}
		if !strings.Contains(check.Fix, "bootstrap") {
			t.Errorf("a switchboard %s carries the fix %q, want the bootstrap that stands it again named", what, check.Fix)
		}
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
	checks, err := p.CheckHost(context.Background(), providerkit.HostCheckRequest{
		Class:     providerkit.ClassProduction,
		Hostnames: []string{"shop.example.com"},
	})
	if err != nil {
		t.Fatalf("CheckHost() = %v, and this same rpc runs on every `ocel deploy`: a standing concern is a report and never a gate", err)
	}
	if len(checks) == 0 {
		t.Fatal("CheckHost() answered nothing at all over a box whose address could not be read, and an empty report is read as a box with nothing wrong")
	}
	for _, check := range checks {
		if check.Verdict != providerkit.HostFail {
			t.Errorf("check %+v passed over a box whose own address could not be read", check)
		}
	}
	if !strings.Contains(checks[0].Finding, "address") {
		t.Errorf("finding = %q, want the address read that failed named", checks[0].Finding)
	}
}

func TestAProxyThatNamedNoSocketAtAllIsNotReadAsACleanNamespace(t *testing.T) {
	t.Parallel()

	check := adminCheck(t, standingOver(boxSaying(map[string]answer{"/proc/net/tcp &&": {stdout: socketTable()}})))
	if check.Verdict != providerkit.HostFail {
		t.Fatalf("verdict = %v (%q), want a failure: a running proxy always holds %s and %s, so a namespace naming nothing is one this box never read rather than one with a clean admin port",
			check.Verdict, check.Finding, caddy.HTTPPort, "443")
	}
	if !strings.Contains(check.Finding, "no listening sockets") {
		t.Errorf("finding = %q, want it to say the proxy named nothing rather than to report the admin port clean", check.Finding)
	}
	if check.Fix == "" {
		t.Error("a proxy that named no socket carries no fix, and there is nothing for an operator to do with the finding alone")
	}
}
