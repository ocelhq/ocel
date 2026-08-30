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
	"strings"
	"testing"
	"time"
)

func pinnedBlocks(t *testing.T, names []string, until time.Duration) (cert, key []byte) {
	t.Helper()

	held, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
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
	raw, err := x509.CreateCertificate(rand.Reader, &template, &template, &held.PublicKey, held)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := x509.MarshalECPrivateKey(held)
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
	if err := os.WriteFile(PinCertificate(at), cert, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(PinKey(at), key, 0o600); err != nil {
		t.Fatal(err)
	}
	return at
}

func TestARealProxyServesAPinnedPairOffTheOneDirectoryTheBoxBindsIntoIt(t *testing.T) {
	stood := proxyStanding(t)

	at := ProxyPins + "/wildcard"
	pinnedPair(t, stood.pins, "wildcard", []string{"*.preview.example.com"})

	state := routed()
	state.Claims = []HostClaim{{Hostname: "pr-7.preview.example.com", Owner: surface, Pointer: pointed}}
	state.Pins = []Pin{{Hostname: "*.preview.example.com", Path: at}}
	rendered, err := RenderProxyConfig(state)
	if err != nil {
		t.Fatalf("RenderProxyConfig() = %v", err)
	}

	if !strings.Contains(string(rendered), proxyPinsMount+"/wildcard"+pinCertificate) {
		t.Fatalf("the config names a pinned pair by a path other than the one the proxy is handed it at:\n%s", rendered)
	}
	write := exec.Command("/bin/sh", "-c", stood.here(stagedWrite(contentSum(proxyBaseline))))
	write.Stdin = strings.NewReader(string(issuedByNobody(t, rendered)))
	if out, err := write.CombinedOutput(); err != nil {
		t.Fatalf("the staged write a deploy makes = %v\n%s", err, out)
	}
	flip := exec.Command(dockerEngine, "exec", ProxyContainer, ProxyHelperMount, "flip", proxyConfigMount)
	if out, err := flip.CombinedOutput(); err != nil {
		t.Fatalf("the proxy stood up as the box stands it would not take a config carrying an operator's pin, so every reshape on a box with one pinned — claim, release and retire alike — fails: %v\n%s\n%s",
			err, out, logsOf(ProxyContainer))
	}

	var read []byte
	for range 100 {
		asked := exec.Command(dockerEngine, "exec", ProxyContainer, ProxyHelperMount, "leaf", "pr-7.preview.example.com")
		if out, err := asked.Output(); err == nil {
			read = out
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(read) == 0 {
		t.Fatalf("the helper read no leaf off the proxy's own :443 for a hostname a pinned pair covers:\n%s", logsOf(ProxyContainer))
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
	if strings.Contains(logsOf(ProxyContainer), "obtain") {
		t.Errorf("the proxy tried to obtain a certificate for a name a pinned pair already covers:\n%s", logsOf(ProxyContainer))
	}
	if strings.Contains(logsOf(ProxyContainer), acmeDirectory) {
		t.Errorf("the proxy reached a public CA from a package-level `go test`: this renders a claim for an example.com name, and what keeps the order off the wire is the load_files suppression this very test exists to check:\n%s", logsOf(ProxyContainer))
	}
}

func TestARealProxyAsksACAForEveryHostnameSomethingOnTheBoxClaims(t *testing.T) {
	state := twoProjects()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	rendered, err := RenderProxyConfig(state)
	if err != nil {
		t.Fatalf("RenderProxyConfig() = %v", err)
	}
	ask := probingConfig(t, issuedByNobody(t, rendered))

	if said := ask(claimed); said.status/100 == 3 {
		t.Errorf("a claimed hostname over plain http answers %d, want it served. Caddy does inject http->https redirect routes into the %s server and says so in its log; what keeps them off every hostname this box serves is that each ocel route is terminal and rendered ahead of them, with the terminal 404 ahead of the appended catch-all. A matcher-less route rendered before the claims, or an ocel route that stops being terminal, turns the redirects on and takes the http-01 challenge and the journey's plain-http leg with them",
			said.status, proxyServer)
	}
	if said := ask("unclaimed.example.com"); said.status != http.StatusNotFound || said.edge != EdgeName {
		t.Errorf("a hostname nothing claims answers %d as %q over plain http, want the box's own refusal on both ports", said.status, said.edge)
	}

	const managing = "enabling automatic TLS certificate management"
	var logs string
	for range 100 {
		if logs = logsOf(probeName(t)); managed(logs, managing, claimed) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !managed(logs, managing, claimed) {
		t.Errorf("the proxy manages certificates for nothing naming %s, so the hostname this box claims is outside its automatic-https subject collection and `proxy:%s` would name a certificate nothing ever asks for:\n%s",
			claimed, claimed, logs)
	}
	if strings.Contains(logs, acmeDirectory) {
		t.Errorf("the proxy reached a public CA from a package-level `go test`: the subject collection is read off the log line above, and the order behind it must never leave this machine:\n%s", logs)
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
	apps, held := config["apps"].(map[string]any)
	if !held {
		t.Fatalf("the rendered config declares no apps to point at a CA of this machine's own:\n%s", rendered)
	}
	tls, _ := apps["tls"].(map[string]any)
	if tls == nil {
		tls = map[string]any{}
	}
	tls["automation"] = map[string]any{"policies": []any{map[string]any{
		"issuers": []any{map[string]any{"module": "acme", "ca": unreachableCA}},
	}}}
	apps["tls"] = tls
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
	state := ProxyState{Grace: DrainWindow, Claims: []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}}
	probingConfig(t, issuedByNobody(t, mustRender(t, state)))

	const managing = "enabling automatic TLS certificate management"
	var logs string
	for range 100 {
		if logs = logsOf(probeName(t)); managed(logs, managing, claimed) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !managed(logs, managing, claimed) {
		t.Errorf("a hostname claimed on this box with nothing yet serving it is outside the automatic-https subject collection, so a project that binds a domain before its first deploy never gets a certificate and `ocel domain add` waits on a name that will never terminate tls:\n%s", logs)
	}
	if strings.Contains(logs, acmeDirectory) {
		t.Errorf("the proxy reached a public CA from a package-level `go test`:\n%s", logs)
	}
}
