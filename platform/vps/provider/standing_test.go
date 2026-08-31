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
