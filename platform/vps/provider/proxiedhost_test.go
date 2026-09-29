package vps_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

type probed struct {
	router  router.Kind
	failure string
}

func (p probed) ServingRouter(context.Context, string) (router.Kind, error) { return p.router, nil }

func (p probed) LastProbeFailure(string) string { return p.failure }

func cloudflareAnswers() map[string][]string {
	answers := hereOnly()
	answers["shop.example.com"] = []string{"104.16.0.1", "2606:4700::1"}
	return answers
}

func TestAHostnameCloudflareForwardsPassesWhenTheSwitchboardAnswersThroughIt(t *testing.T) {
	t.Parallel()

	check := vps.ProxiedVerdict(context.Background(), stubResolver(cloudflareAnswers()), probed{router: switchboard.RouterKind}, "shop.example.com", boxAddress)

	if check.Verdict != provider.HostPass {
		t.Fatalf("verdict = %v (%q), want a pass: the name resolves to Cloudflare, which forwards it to this box", check.Verdict, check.Finding)
	}
	if !strings.Contains(check.Finding, string(switchboard.RouterKind)) {
		t.Errorf("finding = %q, want it to say the switchboard answered", check.Finding)
	}
}

func TestAHostnameCloudflareForwardsFailsWhenNothingOnThisBoxAnswersThroughIt(t *testing.T) {
	t.Parallel()

	for what, serving := range map[string]probed{
		"another router": {router: "cloudfront"},
		"nothing at all": {failure: "tls: handshake failure"},
	} {
		check := vps.ProxiedVerdict(context.Background(), stubResolver(cloudflareAnswers()), serving, "shop.example.com", boxAddress)
		if check.Verdict != provider.HostFail {
			t.Errorf("verdict over %s = %v (%q), want a failure", what, check.Verdict, check.Finding)
		}
		if check.Fix == "" || !strings.Contains(check.Fix, boxAddress) {
			t.Errorf("fix over %s = %q, want the address the proxied record should forward to", what, check.Fix)
		}
		if serving.failure != "" && !strings.Contains(check.Finding, serving.failure) {
			t.Errorf("finding over %s = %q, want the last failure named", what, check.Finding)
		}
	}
}

func TestAHostnameCloudflareShouldForwardButDoesNotResolveNeedsTheRecordAdded(t *testing.T) {
	t.Parallel()

	check := vps.ProxiedVerdict(context.Background(), stubResolver(hereOnly()), probed{}, "shop.example.com", boxAddress)

	if check.Verdict != provider.HostNeedsAction {
		t.Fatalf("verdict = %v (%q), want the record asked for", check.Verdict, check.Finding)
	}
	if !strings.Contains(check.Fix, "ocel domain add") {
		t.Errorf("fix = %q, want the command that writes the proxied record", check.Fix)
	}
}
