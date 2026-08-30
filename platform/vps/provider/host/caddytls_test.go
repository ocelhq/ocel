package host

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
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

func pinnedPair(t *testing.T, dir, name string, names []string) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
	}
	raw, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	at := filepath.Join(dir, name)
	if err := os.WriteFile(PinCertificate(at), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(PinKey(at), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sealed}), 0o600); err != nil {
		t.Fatal(err)
	}
	return at
}

func TestARealProxyServesAPinnedPairAndTheHelperReadsItOffTheHandshake(t *testing.T) {
	engineOrSkip(t)
	dir := seenByTheEngine(t)

	at := pinnedPair(t, dir, "wildcard", []string{"*.preview.example.com"})
	state := ProxyState{
		Grace:  DrainWindow,
		Claims: []HostClaim{{Hostname: "pr-7.preview.example.com", Owner: surface}},
		Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: "shop-web-2222:" + AppPort}},
		Pins:   []Pin{{Hostname: "*.preview.example.com", Path: at}},
	}
	rendered, err := RenderProxyConfig(state)
	if err != nil {
		t.Fatalf("RenderProxyConfig() = %v", err)
	}
	config := filepath.Join(dir, "caddy.json")
	if err := os.WriteFile(config, rendered, 0o644); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, proxyHelperName)
	if err := os.WriteFile(helper, proxyHelper(ArchAMD64), 0o755); err != nil {
		t.Fatal(err)
	}

	name := probeName(t)
	exec.Command(dockerEngine, "rm", "--force", name).Run()
	stood, err := exec.Command(dockerEngine, "run", "--rm", "--detach", "--name", name,
		"--publish", "127.0.0.1::80",
		"--volume", config+":"+proxyConfigMount+":ro",
		"--volume", helper+":"+ProxyHelperMount+":ro",
		"--volume", dir+":"+dir+":ro",
		ProxyImage, "caddy", "run", "--config", proxyConfigMount).CombinedOutput()
	if err != nil {
		t.Skipf("this machine's engine will not run %s: %s", ProxyImage, stood)
	}
	t.Cleanup(func() { exec.Command(dockerEngine, "rm", "--force", name).Run() })

	standing := false
	published, err := exec.Command(dockerEngine, "port", name, "80/tcp").Output()
	if err != nil {
		t.Fatalf("read the port the probe proxy publishes: %v\n%s", err, logsOf(name))
	}
	at80 := "http://" + strings.TrimSpace(strings.Split(string(published), "\n")[0])
	for range 100 {
		if said, err := http.Get(at80); err == nil {
			said.Body.Close()
			standing = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !standing {
		t.Fatalf("the probe proxy never answered:\n%s", logsOf(name))
	}

	var read []byte
	for range 100 {
		asked := exec.Command(dockerEngine, "exec", name, ProxyHelperMount, "leaf", "pr-7.preview.example.com")
		if out, err := asked.Output(); err == nil {
			read = out
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(read) == 0 {
		t.Fatalf("the helper read no leaf off the proxy's own :443 for a hostname a pinned pair covers:\n%s", logsOf(name))
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
	if strings.Contains(logsOf(name), "obtain") {
		t.Errorf("the proxy tried to obtain a certificate for a name a pinned pair already covers:\n%s", logsOf(name))
	}
}

func TestARealProxyAsksACAForEveryHostnameSomethingOnTheBoxClaims(t *testing.T) {
	state := twoProjects()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface}}
	ask := probing(t, state)

	if said := ask(claimed); said.status/100 == 3 {
		t.Errorf("a claimed hostname over plain http answers %d, want it served: the box's server listens on both %s and %s, which is what keeps the http-01 challenge, the box's own 404 and the journey's plain-http leg answering on the same routes",
			said.status, proxyPort, proxyTLSPort)
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
}

func managed(logs, managing, hostname string) bool {
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, managing) && strings.Contains(line, hostname) {
			return true
		}
	}
	return false
}
