package host

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func pinnedBlocks(t *testing.T, names []string, until time.Duration) (cert, key []byte) {
	t.Helper()

	signer, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(until),
	}
	raw, err := x509.CreateCertificate(rand.Reader, &template, &template, &signer.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := x509.MarshalECPrivateKey(signer)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sealed})
}

func pinnedPair(t *testing.T, dir, name string, names []string) string {
	t.Helper()

	cert, key := pinnedBlocks(t, names, 90*24*time.Hour)
	at := filepath.Join(dir, name)
	if err := os.WriteFile(caddy.PinCertificate(at), cert, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caddy.PinKey(at), key, 0o600); err != nil {
		t.Fatal(err)
	}
	return at
}

func TestARealProxyServesAPinnedPairOffTheOneDirectoryTheBoxBindsIntoIt(t *testing.T) {
	proxyBox := aLiveProxy(t)

	at := caddy.PinsDir + "/wildcard"
	pinnedPair(t, proxyBox.pins, "wildcard", []string{"*.preview.example.com"})

	state := routed()
	state.Claims = []HostClaim{{Hostname: "pr-7.preview.example.com", Owner: surface, Pointer: pointed}}
	state.Pins = []Pin{{Hostname: "*.preview.example.com", Path: at}}
	rendered, err := RenderProxyConfig(caddy.Builtin{}, state)
	if err != nil {
		t.Fatalf("RenderProxyConfig(caddy.Builtin{}, ) = %v", err)
	}

	if !strings.Contains(string(rendered), caddy.PinCertificate(caddy.PinsMount+"/wildcard")) {
		t.Fatalf("the config names a pinned pair by a path other than the one the proxy is handed it at:\n%s", rendered)
	}
	proxyBox.stages(t, routingTableItem().Content, state, issuedByNobody(t, rendered))
	proxyBox.drives(t, "load", proxyBox.table)
	reload := exec.Command(dockerEngine, "exec", proxyBox.name, "caddy", "reload", "--config", caddy.ConfigMount, "--address", "unix/"+caddy.AdminSocket)
	if out, err := reload.CombinedOutput(); err != nil {
		t.Fatalf("the proxy run as the box runs it would not take a config containing an operator's pin, so every reshape on a box with one pinned — claim, release and retire alike — fails: %v\n%s\n%s",
			err, out, logsOf(proxyBox.name))
	}

	var read []byte
	for range 100 {
		asked := exec.Command(proxyBox.binary, "leaf", "pr-7.preview.example.com")
		if out, err := asked.Output(); err == nil {
			read = out
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(read) == 0 {
		t.Fatalf("the switchboard read no leaf off the box's own :443 for a hostname a pinned pair covers:\n%s", logsOf(proxyBox.name))
	}
	block, _ := pem.Decode(read)
	if block == nil {
		t.Fatalf("the helper answered %q, want a pem certificate block", read)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse what the helper read: %v", err)
	}
	if leaf.Subject.CommonName != "*.preview.example.com" {
		t.Errorf("the proxy served %q for pr-7.preview.example.com, want the pinned pair: a loaded certificate suppresses automatic management for its subject, so nothing here asks a CA",
			leaf.Subject.CommonName)
	}
	if strings.Contains(logsOf(proxyBox.name), "obtain") {
		t.Errorf("the proxy tried to obtain a certificate for a name a pinned pair already covers:\n%s", logsOf(proxyBox.name))
	}
	if strings.Contains(logsOf(proxyBox.name), acmeDirectory) {
		t.Errorf("the proxy reached a public CA from a package-level `go test`: this renders a claim for an example.com name, and what keeps the order off the wire is the load_files suppression this very test exists to check:\n%s", logsOf(proxyBox.name))
	}
}

func TestARealProxyOrdersOnTheFirstHandshakeForAClaimedHostnameAndNeverForAnUnclaimedOne(t *testing.T) {
	state := twoProjects()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	proxyBox, ask := probedBox(t, state, issuedByNobody(t, mustRender(t, state)))

	if said := ask(claimed); said.status/100 == 3 {
		t.Errorf("a claimed hostname over plain http answers %d, want it served. Caddy appends its catch-all http->https redirect behind the one terminal forward this config renders, so a forward that stops being terminal turns the redirect on and takes the http-01 challenge and the journey's plain-http leg with it",
			said.status)
	}
	if said := ask("unclaimed.example.com"); said.status != http.StatusNotFound || said.edge != switchboard.EdgeName {
		t.Errorf("a hostname nothing claims answers %d as %q over plain http, want the box's own refusal on both ports", said.status, said.edge)
	}

	proxyBox.handshake("unclaimed.example.com")
	proxyBox.handshake(claimed)
	logs := proxyBox.ordered(t, claimed)
	if managed(logs, onDemandOrder, "unclaimed.example.com") {
		t.Errorf("the proxy ordered a certificate for a hostname nothing on this box claims, so anyone who points a name at the box spends its CA allowance:\n%s", logs)
	}
	if strings.Contains(logs, acmeDirectory) {
		t.Errorf("the proxy reached a public CA from a package-level `go test`: the order is read off the log line above, and it must never leave this machine:\n%s", logs)
	}
}

const unreachableCA = "https://127.0.0.1:9/directory"

const acmeDirectory = "acme-v02.api.letsencrypt.org"

func issuedByNobody(t *testing.T, rendered []byte) []byte {
	t.Helper()

	var config map[string]any
	if err := json.Unmarshal(rendered, &config); err != nil {
		t.Fatal(err)
	}
	apps, _ := config["apps"].(map[string]any)
	tls, _ := apps["tls"].(map[string]any)
	automation, _ := tls["automation"].(map[string]any)
	policies, _ := automation["policies"].([]any)
	caught := false
	for _, entry := range policies {
		policy := entry.(map[string]any)
		if _, scoped := policy["subjects"]; !scoped {
			policy["issuers"] = []any{map[string]any{"module": "acme", "ca": unreachableCA}}
			caught = true
		}
	}
	if !caught {
		t.Fatalf("the rendered config declares no catch-all policy to point at a CA of this machine's own:\n%s", rendered)
	}
	written, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return written
}

func managed(logs, managing, hostname string) bool {
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, managing) && strings.Contains(line, hostname) {
			return true
		}
	}
	return false
}

func TestAHostnameClaimedBeforeAnythingServesItIsOrderedForAllTheSame(t *testing.T) {
	state := RoutingTable{Grace: DrainWindow, Claims: []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}}
	proxyBox, _ := probedBox(t, state, issuedByNobody(t, mustRender(t, state)))

	proxyBox.handshake(claimed)
	if logs := proxyBox.ordered(t, claimed); strings.Contains(logs, acmeDirectory) {
		t.Errorf("the proxy reached a public CA from a package-level `go test`:\n%s", logs)
	}
}

const onDemandOrder = "obtaining new certificate"

func (p liveProxy) handshake(hostname string) {
	_ = exec.Command(p.binary, "leaf", hostname).Run()
}

func (p liveProxy) ordered(t *testing.T, hostname string) string {
	t.Helper()

	var logs string
	for range 100 {
		if logs = logsOf(p.name); managed(logs, onDemandOrder, hostname) {
			return logs
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the proxy never ordered a certificate for %s on its first handshake, so a domain bound on this box waits on a name that will never terminate tls:\n%s", hostname, logs)
	return logs
}

func (p liveProxy) servedLeaf(t *testing.T, hostname string) *x509.Certificate {
	t.Helper()

	var read []byte
	for range 100 {
		if out, err := exec.Command(p.binary, "leaf", hostname).Output(); err == nil {
			read = out
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	block, _ := pem.Decode(read)
	if block == nil {
		t.Fatalf("the proxy served no certificate for %s:\n%s", hostname, logsOf(p.name))
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse the leaf served for %s: %v", hostname, err)
	}
	return leaf
}

func TestARealProxyServesAPinAddedOverAHostnameItAlreadyOrderedFor(t *testing.T) {
	const (
		hostname = "shop.ocel.home.arpa"
		wildcard = "*.ocel.home.arpa"
	)
	state := routed()
	state.Claims = []HostClaim{{Hostname: hostname, Owner: surface, Pointer: pointed}}
	proxyBox, _ := probedBox(t, state, issuedByNobody(t, mustRender(t, state)))
	if ordered := proxyBox.servedLeaf(t, hostname); ordered.Subject.CommonName == wildcard || !slices.Contains(ordered.DNSNames, hostname) {
		t.Fatalf("the proxy served %q %v for %s before any pin, want the one it ordered for that name: nothing below is a pin taking over from it",
			ordered.Subject.CommonName, ordered.DNSNames, hostname)
	}

	written := mustWrite(t, state)
	pinnedPair(t, proxyBox.pins, "wildcard", []string{wildcard})
	state.Pins = []Pin{{Hostname: wildcard, Path: caddy.PinsDir + "/wildcard"}}
	proxyBox.stages(t, written, state, issuedByNobody(t, mustRender(t, state)))
	proxyBox.drives(t, "load", proxyBox.table)
	proxyBox.reloads(t)

	if served := proxyBox.servedLeaf(t, hostname); served.Subject.CommonName != wildcard {
		t.Errorf("the proxy serves %q for %s after %s was pinned over it, want the pin: the certificate it ordered is the exact match caddy serves ahead of any wildcard, and it goes on serving and renewing it until caddy restarts",
			served.Subject.CommonName, hostname, wildcard)
	}
}
