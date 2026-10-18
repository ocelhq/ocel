package host

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func pulledCertificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(7),
		DNSNames:     []string{"example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), base64.StdEncoding.EncodeToString(der)
}

func TestAShieldedHostnameIsAnsweredOnlyToAClientPresentingACertificateItTrusts(t *testing.T) {
	t.Parallel()

	certificate, trusted := pulledCertificate(t)
	table := releasing()
	table.Claims = []boxapi.Claim{
		{Hostname: claimed, Owner: surface, Pointer: pointed, App: "web"},
		{Hostname: "storage." + claimed, Owner: surface, Pointer: pointed, App: "storage"},
	}
	table.Shields = []boxapi.Shield{{Hostname: claimed, Owner: surface, ClientCAs: []string{certificate}}}

	rendered := string(mustRender(t, table))
	if strings.Count(rendered, trusted) != 1 {
		t.Errorf("the proxy config trusts the client certificate %d times, want only for %s: its store hostname is reached without the edge in front", strings.Count(rendered, trusted), claimed)
	}

	written, err := routing.WriteRoutingTable(table)
	if err != nil {
		t.Fatalf("WriteRoutingTable: %v", err)
	}
	read, err := routing.ReadRoutingTable(written)
	if err != nil {
		t.Fatalf("ReadRoutingTable: %v", err)
	}
	if len(read.Shields) != 1 || !slices.Equal(read.Shields[0].ClientCAs, []string{certificate}) {
		t.Errorf("the table reads back shields %+v, want the client certificate kept: the switchboard reads the same table and must accept it", read.Shields)
	}
}

func TestARealProxyRedirectsAShieldedHostnameOverPlainHTTPToHTTPSAndStillServesTheRest(t *testing.T) {
	certificate, _ := pulledCertificate(t)
	state := twoProjects()
	state.Claims = []boxapi.Claim{
		{Hostname: claimed, Owner: surface, Pointer: pointed},
		{Hostname: "blog.example.com", Owner: otherSurface, Pointer: pointed},
	}
	state.Shields = []boxapi.Shield{{Hostname: claimed, Owner: surface, ClientCAs: []string{certificate}}}
	ask := probing(t, state)

	if said := ask(claimed); said.status != http.StatusPermanentRedirect || said.location != "https://"+claimed+"/" || said.router == boxapi.SwitchboardRouter {
		t.Errorf("a shielded hostname over plain http answered %d to %q from %q, want 308 to https://%s/ before the switchboard: :80 carries no client certificate, so a request that skips the edge must reach no app, and a visitor Cloudflare forwarded over http lands on https", said.status, said.location, said.router, claimed)
	}
	if said := ask("blog.example.com"); said.router != boxapi.SwitchboardRouter {
		t.Errorf("a hostname nothing shields over plain http answered %d from %q, want the switchboard: its http-01 challenge and plain-http leg go through here", said.status, said.router)
	}
}

func TestMergingAShieldCarriesItsCertificatesToEveryHostnameThatTrustedOneOfThem(t *testing.T) {
	t.Parallel()

	shields := []boxapi.Shield{
		{Hostname: "shop.example.com", Owner: "ocel-shop-production", ClientCAs: []string{"old"}},
		{Hostname: "blog.example.com", Owner: "ocel-blog-production", ClientCAs: []string{"old"}},
		{Hostname: "other.example.org", Owner: "ocel-other-production", ClientCAs: []string{"another zone"}},
	}

	rotated := routing.MergeShield(shields, boxapi.Shield{Hostname: "shop.example.com", Owner: "ocel-shop-production", ClientCAs: []string{"new", "old"}})
	trusts := map[string][]string{}
	for _, shield := range rotated {
		trusts[shield.Hostname] = shield.ClientCAs
	}
	if !slices.Equal(trusts["blog.example.com"], []string{"new", "old"}) {
		t.Errorf("blog.example.com trusts %v, want the successor too: the zone presents one certificate to every origin it forwards to, and switches them all at once", trusts["blog.example.com"])
	}
	if !slices.Equal(trusts["other.example.org"], []string{"another zone"}) {
		t.Errorf("other.example.org trusts %v, want its own zone's certificate left alone", trusts["other.example.org"])
	}

	retired := routing.MergeShield(rotated, boxapi.Shield{Hostname: "shop.example.com", Owner: "ocel-shop-production", ClientCAs: []string{"new"}})
	retired = routing.MergeShield(retired, boxapi.Shield{Hostname: "blog.example.com", Owner: "ocel-blog-production", ClientCAs: []string{"new"}})
	for _, shield := range retired {
		if shield.Hostname == "blog.example.com" && !slices.Equal(shield.ClientCAs, []string{"new"}) {
			t.Errorf("blog.example.com trusts %v once its own claim retired the old certificate, want the successor alone", shield.ClientCAs)
		}
	}
}

func TestMergingAShieldFromOneZoneKeepsEveryCertificateAnotherZoneStillPresents(t *testing.T) {
	t.Parallel()

	shields := []boxapi.Shield{{Hostname: "shop.example.org", Owner: "ocel-org-production", ClientCAs: []string{"uploaded to both", "org's own"}}}
	merged := routing.MergeShield(shields, boxapi.Shield{Hostname: "shop.example.com", Owner: "ocel-com-production", ClientCAs: []string{"uploaded to both"}})
	for _, shield := range merged {
		if shield.Hostname == "shop.example.org" && !slices.Contains(shield.ClientCAs, "org's own") {
			t.Errorf("shop.example.org trusts %v once example.com was shielded, want its own zone's certificate still: example.org presents it, and dropping it refuses every request forwarded there", shield.ClientCAs)
		}
	}
}

func TestMergingAShieldAgainKeepsTheOriginCertificateItAlreadyHolds(t *testing.T) {
	t.Parallel()

	held := []boxapi.Shield{{Hostname: "shop.example.com", Owner: "ocel-shop-production", ClientCAs: []string{"zone"}, OriginCertificate: boxapi.CertificatePair{Certificate: "ORIGIN", Key: "KEY"}}}
	again := routing.MergeShield(held, boxapi.Shield{Hostname: "shop.example.com", Owner: "ocel-shop-production", ClientCAs: []string{"zone", "successor"}})
	if len(again) != 1 || again[0].OriginCertificate.Certificate != "ORIGIN" || again[0].OriginCertificate.Key != "KEY" {
		t.Errorf("shielding shop.example.com again leaves %+v, want the origin certificate it holds kept: a claim carries one only when the origin needs a new one", again)
	}
}

func TestABoxBehindYourProxyRefusesToShieldAHostnameYourProxyAnswersWithoutACertificate(t *testing.T) {
	t.Parallel()

	box := newBench(nil)
	probed := 0
	answers := "switchboard\n"
	box.answer = func(command string) (ssh.Result, bool) {
		if strings.Contains(command, shell.Quote("probe")) {
			probed++
			return ssh.Result{Stdout: answers}, true
		}
		return ssh.Result{}, false
	}
	yours := box.fronted(front.Proxy{Manual: &front.Manual{Port: 8480}})

	err := yours.RefuseUnshielded(context.Background(), "shop.example.com")
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady || !strings.Contains(refused.Message, "client certificate") {
		t.Errorf("RefuseUnshielded over a proxy that answers without a certificate = %v, want refused naming the client certificate to require", err)
	}

	answers = ""
	box.answer = func(command string) (ssh.Result, bool) {
		if strings.Contains(command, shell.Quote("probe")) {
			probed++
			return ssh.Result{Code: 3, Stderr: "shop.example.com at 127.0.0.1:443: remote error: tls: certificate required\n"}, true
		}
		return ssh.Result{}, false
	}
	if err := yours.RefuseUnshielded(context.Background(), "shop.example.com"); err != nil {
		t.Errorf("RefuseUnshielded over a proxy that refuses a client with no certificate = %v, want nil", err)
	}

	before := probed
	if err := box.host().RefuseUnshielded(context.Background(), "shop.example.com"); err != nil || probed != before {
		t.Errorf("RefuseUnshielded over ocel's own proxy = %v after %d probes, want nil and no probe: ocel renders the shield itself", err, probed-before)
	}
}
