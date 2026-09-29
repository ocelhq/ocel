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
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
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
	table.Claims = []HostClaim{
		{Hostname: claimed, Owner: surface, Pointer: pointed, App: "web"},
		{Hostname: "storage." + claimed, Owner: surface, Pointer: pointed, App: "storage"},
	}
	table.Shields = []Shield{{Hostname: claimed, Owner: surface, ClientCertificates: []string{certificate}}}

	rendered := string(mustRender(t, table))
	if strings.Count(rendered, trusted) != 1 {
		t.Errorf("the proxy config trusts the client certificate %d times, want only for %s: its store hostname is reached without the edge in front", strings.Count(rendered, trusted), claimed)
	}

	written, err := WriteRoutingTable(table)
	if err != nil {
		t.Fatalf("WriteRoutingTable: %v", err)
	}
	read, err := ReadRoutingTable(written)
	if err != nil {
		t.Fatalf("ReadRoutingTable: %v", err)
	}
	if len(read.Shields) != 1 || !slices.Equal(read.Shields[0].ClientCertificates, []string{certificate}) {
		t.Errorf("the table reads back shields %+v, want the client certificate kept: the switchboard reads the same table and must accept it", read.Shields)
	}
}

func TestARealProxyRefusesAShieldedHostnameOverPlainHTTPAndStillServesTheRest(t *testing.T) {
	certificate, _ := pulledCertificate(t)
	state := twoProjects()
	state.Claims = []HostClaim{
		{Hostname: claimed, Owner: surface, Pointer: pointed},
		{Hostname: "blog.example.com", Owner: otherSurface, Pointer: pointed},
	}
	state.Shields = []Shield{{Hostname: claimed, Owner: surface, ClientCertificates: []string{certificate}}}
	ask := probing(t, state)

	if said := ask(claimed); said.status != http.StatusForbidden || said.router == switchboard.RouterKind {
		t.Errorf("a shielded hostname over plain http answered %d from %q, want 403 before the switchboard: :80 carries no client certificate, so a request that skips the edge reaches the app", said.status, said.router)
	}
	if said := ask("blog.example.com"); said.router != switchboard.RouterKind {
		t.Errorf("a hostname nothing shields over plain http answered %d from %q, want the switchboard: its http-01 challenge and plain-http leg go through here", said.status, said.router)
	}
}

func TestMergingAShieldCarriesItsCertificatesToEveryHostnameThatTrustedOneOfThem(t *testing.T) {
	t.Parallel()

	shields := []Shield{
		{Hostname: "shop.example.com", Owner: "ocel-shop-production", ClientCertificates: []string{"old"}},
		{Hostname: "blog.example.com", Owner: "ocel-blog-production", ClientCertificates: []string{"old"}},
		{Hostname: "other.example.org", Owner: "ocel-other-production", ClientCertificates: []string{"another zone"}},
	}

	rotated := MergeShield(shields, Shield{Hostname: "shop.example.com", Owner: "ocel-shop-production", ClientCertificates: []string{"new", "old"}})
	trusts := map[string][]string{}
	for _, shield := range rotated {
		trusts[shield.Hostname] = shield.ClientCertificates
	}
	if !slices.Equal(trusts["blog.example.com"], []string{"new", "old"}) {
		t.Errorf("blog.example.com trusts %v, want the successor too: the zone presents one certificate to every origin it forwards to, and switches them all at once", trusts["blog.example.com"])
	}
	if !slices.Equal(trusts["other.example.org"], []string{"another zone"}) {
		t.Errorf("other.example.org trusts %v, want its own zone's certificate left alone", trusts["other.example.org"])
	}

	retired := MergeShield(rotated, Shield{Hostname: "shop.example.com", Owner: "ocel-shop-production", ClientCertificates: []string{"new"}})
	for _, shield := range retired {
		if shield.Hostname == "blog.example.com" && !slices.Equal(shield.ClientCertificates, []string{"new"}) {
			t.Errorf("blog.example.com trusts %v once the old certificate is retired, want the successor alone", shield.ClientCertificates)
		}
	}
}

func TestMergingAShieldAgainKeepsTheOriginCertificateItAlreadyHolds(t *testing.T) {
	t.Parallel()

	held := []Shield{{Hostname: "shop.example.com", Owner: "ocel-shop-production", ClientCertificates: []string{"zone"}, OriginCertificate: proxy.CertificatePair{Certificate: "ORIGIN", Key: "KEY"}}}
	again := MergeShield(held, Shield{Hostname: "shop.example.com", Owner: "ocel-shop-production", ClientCertificates: []string{"zone", "successor"}})
	if len(again) != 1 || again[0].OriginCertificate.Certificate != "ORIGIN" || again[0].OriginCertificate.Key != "KEY" {
		t.Errorf("shielding shop.example.com again leaves %+v, want the origin certificate it holds kept: a claim carries one only when the origin needs a new one", again)
	}
}

func TestABoxBehindYourProxyRefusesToShieldAHostnameYourProxyAnswersWithoutACertificate(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	probed := 0
	answers := "switchboard\n"
	box.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, quoted("probe")) {
			probed++
			return session.Result{Stdout: answers}, true
		}
		return session.Result{}, false
	}
	yours := box.fronted(Front{Manual: &ManualFront{Port: 8480}})

	err := yours.RefuseUnshielded(context.Background(), "shop.example.com")
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady || !strings.Contains(refused.Message, "client certificate") {
		t.Errorf("RefuseUnshielded over a proxy that answers without a certificate = %v, want refused naming the client certificate to require", err)
	}

	answers = ""
	box.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, quoted("probe")) {
			probed++
			return session.Result{Code: 3, Stderr: "shop.example.com at 127.0.0.1:443: remote error: tls: certificate required\n"}, true
		}
		return session.Result{}, false
	}
	if err := yours.RefuseUnshielded(context.Background(), "shop.example.com"); err != nil {
		t.Errorf("RefuseUnshielded over a proxy that refuses a client with no certificate = %v, want nil", err)
	}

	before := probed
	if err := box.host().RefuseUnshielded(context.Background(), "shop.example.com"); err != nil || probed != before {
		t.Errorf("RefuseUnshielded over ocel's own proxy = %v after %d probes, want nil and no probe: ocel renders the shield itself", err, probed-before)
	}
}
