package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

func pinnedPair(t *testing.T) ([]byte, []byte) {
	t.Helper()
	signer, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "shop.example.com"},
		DNSNames:     []string{"shop.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
	}
	raw, err := x509.CreateCertificate(rand.Reader, &template, &template, &signer.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(signer)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
}

func pinsDir(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func printedPin(t *testing.T, dir string, argv ...string) (ran, boxapi.PinnedPair) {
	t.Helper()
	var stdout, stderr strings.Builder
	code := printPin(dir, argv, &stdout, &stderr)
	got := ran{code: code, stdout: stdout.String(), stderr: stderr.String()}
	var pair boxapi.PinnedPair
	if code == 0 {
		if err := json.Unmarshal([]byte(got.stdout), &pair); err != nil {
			t.Fatalf("pin %q printed %q, which is not one pinned pair: %v", argv, got.stdout, err)
		}
	}
	return got, pair
}

func TestThePinCommandPrintsTheCertificateAndWhetherTheKeyBesideItMatchesNeverTheKey(t *testing.T) {
	t.Parallel()

	certificate, key := pinnedPair(t)
	_, otherKey := pinnedPair(t)
	for name, c := range map[string]struct {
		files   map[string][]byte
		matches bool
	}{
		"a matching pair":                 {files: map[string][]byte{"shop.crt": certificate, "shop.key": key}, matches: true},
		"a key that is another's":         {files: map[string][]byte{"shop.crt": certificate, "shop.key": otherKey}},
		"no key at all":                   {files: map[string][]byte{"shop.crt": certificate}},
		"a certificate file with its key": {files: map[string][]byte{"shop.crt": append(append([]byte{}, key...), certificate...), "shop.key": key}, matches: true},
	} {
		got, pair := printedPin(t, pinsDir(t, c.files), "shop")
		if got.code != 0 {
			t.Errorf("%s: pin shop = %+v, want the pair read", name, got)
			continue
		}
		if pair.Certificate != string(certificate) {
			t.Errorf("%s: pin shop printed the certificate %q, want %q", name, pair.Certificate, certificate)
		}
		if pair.KeyMatches != c.matches {
			t.Errorf("%s: pin shop says the key matches = %v, want %v", name, pair.KeyMatches, c.matches)
		}
		if strings.Contains(got.stdout, "PRIVATE KEY") || strings.Contains(got.stdout, strings.TrimSpace(string(key))[40:80]) {
			t.Errorf("%s: pin shop printed %q, which carries the private key across the session", name, got.stdout)
		}
	}
}

func TestThePinCommandReadsOnlyANameDirectlyUnderThePinsDirectory(t *testing.T) {
	t.Parallel()

	certificate, key := pinnedPair(t)
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "outside.crt"), certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "outside.key"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "certs")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{},
		{"../outside"},
		{".."},
		{"."},
		{""},
		{"nested/shop"},
		{filepath.Join(parent, "outside")},
		{"shop", "shop"},
		{"missing"},
	} {
		got, _ := printedPin(t, dir, argv...)
		if got.code != boxapi.ExitRefused || got.stdout != "" || !strings.HasPrefix(got.stderr, pinNamed+": ") {
			t.Errorf("pin %q = %+v, want it refused with nothing printed", argv, got)
		}
	}
}
