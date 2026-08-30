package certs_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/certs"
)

func TestTheHandleNamesWhoIsOnTheHookWhenItExpires(t *testing.T) {
	t.Parallel()

	proxied := certs.ProxyHandle("shop.example.com")
	if proxied != "proxy:shop.example.com" {
		t.Errorf("ProxyHandle() = %q, want the scheme-prefixed handle a box mints", proxied)
	}
	if got, ok := certs.Serving(proxied); !ok || got != "shop.example.com" {
		t.Errorf("Serving(%q) = %q, %v, want the hostname back", proxied, got, ok)
	}
	if _, pinned := certs.Pinned(proxied); pinned {
		t.Errorf("%q reads as a pinned pair, and the whole of the vocabulary is which of the two renews it", proxied)
	}

	held := certs.PinHandle("/etc/ocel/preview/certs/wildcard")
	if held != "pem:/etc/ocel/preview/certs/wildcard" {
		t.Errorf("PinHandle() = %q", held)
	}
	if certs.Renewal(held) == certs.Renewal(proxied) {
		t.Error("a pinned pair and one the proxy obtained name the same renewer, and that is the only distinction an operator has to act on")
	}
	if !strings.Contains(certs.Renewal(held), "you") {
		t.Errorf("Renewal(a pinned pair) = %q, want it to say the operator renews it", certs.Renewal(held))
	}
}

func TestAWildcardPinCoversOneLabelAndNoDeeperName(t *testing.T) {
	t.Parallel()

	leaf := certs.Leaf{Domains: []string{"*.preview.example.com", "shop.example.com"}}
	for hostname, want := range map[string]bool{
		"pr-7.preview.example.com":     true,
		"shop.example.com":             true,
		"SHOP.example.com":             true,
		"a.b.preview.example.com":      false,
		"preview.example.com":          false,
		"preview.example.com.evil.com": false,
		"www.shop.example.com":         false,
	} {
		if got := leaf.Covers(hostname); got != want {
			t.Errorf("Covers(%q) = %v, want %v", hostname, got, want)
		}
	}
}

func TestABlockThatIsNotACertificateIsRefusedRatherThanReadAsOne(t *testing.T) {
	t.Parallel()

	for what, block := range map[string]string{
		"a private key":     "-----BEGIN EC PRIVATE KEY-----\nMHcCAQE=\n-----END EC PRIVATE KEY-----\n",
		"nothing at all":    "",
		"an unparsed chain": "-----BEGIN CERTIFICATE-----\nbm90IGEgY2VydGlmaWNhdGU=\n-----END CERTIFICATE-----\n",
	} {
		if _, err := certs.Parse("the pinned certificate", []byte(block)); err == nil {
			t.Errorf("Parse(%s) = nil, want a refusal naming what was read", what)
		}
	}
}

func TestExpiryIsReportedAgainstTheRenewalWindowAndNeverGuessed(t *testing.T) {
	t.Parallel()

	now := time.Unix(1800000000, 0)
	fresh := certs.Leaf{NotAfter: now.Add(60 * 24 * time.Hour)}
	soon := certs.Leaf{NotAfter: now.Add(11 * 24 * time.Hour)}
	gone := certs.Leaf{NotAfter: now.Add(-time.Second)}

	if fresh.ExpiringSoon(now) || !soon.ExpiringSoon(now) || !gone.ExpiringSoon(now) {
		t.Error("the renewal window does not separate a certificate an operator must act on from one they need not")
	}
	if fresh.Expired(now) || gone.Expired(now) == false {
		t.Error("an expired certificate reads as live, and a box would serve a hostname under it")
	}
	if (certs.Leaf{}).ExpiresAt() != 0 {
		t.Error("a certificate carrying no NotAfter reports an expiry anyway")
	}
}
