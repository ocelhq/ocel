package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh/sshtest"
)

func TestAPinnedPairWhoseKeyIsNotTheCertificatesIsRefusedBeforeTheProxyLoadsIt(t *testing.T) {
	t.Parallel()

	pin := boxapi.Pin{Hostname: "shop.example.com", Path: boxapi.PinsDir + "/shop"}
	for verdict, refuses := range map[string]bool{pairMatched: false, pairMismatched: true, pairUnchecked: false, "": false} {
		leaf := pinnedLeaf(t, pin.Hostname)
		box := sshtest.New(func(command string) (ssh.Result, bool) {
			switch {
			case strings.Contains(command, "cat "+shell.Quote(boxapi.PinCertificate(pin.Path))):
				return ssh.Result{Stdout: string(leaf)}, true
			case strings.Contains(command, "openssl pkey -in "+shell.Quote(boxapi.PinKey(pin.Path))):
				return ssh.Result{Stdout: verdict + "\n"}, true
			}
			return ssh.Result{}, false
		})
		_, err := NewPins(box.Open(), []boxapi.Pin{pin}).Verified(context.Background())
		var refused refusal.Refusal
		if refused := errors.As(err, &refused); refused != refuses {
			t.Errorf("a pair the box reports %q vouches as %v, want refused=%v: a mismatched pair is one the proxy refuses at the cutover, taking every hostname pinned to it off the air", verdict, err, refuses)
		}
		if refuses && (!strings.Contains(err.Error(), boxapi.PinKey(pin.Path)) || !strings.Contains(err.Error(), boxapi.PinCertificate(pin.Path))) {
			t.Errorf("the refusal reads %q and names neither the key nor the certificate", err)
		}
		for _, command := range box.Commands() {
			if strings.Contains(command, "cat "+shell.Quote(boxapi.PinKey(pin.Path))) || (strings.Contains(command, boxapi.PinKey(pin.Path)) && !strings.Contains(command, "-pubout")) {
				t.Errorf("checking the pair ran %q, which brings the private key across the session rather than a digest of its public half", command)
			}
		}
	}
}

func TestThePairCheckReadsOnlyPublicHalvesAndSaysWhenItCouldNotRun(t *testing.T) {
	t.Parallel()

	command := pairCommand(boxapi.PinsDir + "/shop")
	for what, wanted := range map[string]string{
		"the certificate's public key":          "openssl x509 -in " + shell.Quote(boxapi.PinsDir+"/shop.crt") + " -noout -pubkey",
		"the key's public half":                 "openssl pkey -in " + shell.Quote(boxapi.PinsDir+"/shop.key") + " -pubout",
		"a verdict when openssl is absent":      "echo " + pairUnchecked,
		"a digest rather than the bytes":        "sha256sum",
		"one word for a pair that does not fit": "echo " + pairMismatched,
	} {
		if !strings.Contains(command, wanted) {
			t.Errorf("the pair check runs\n%s\nwhich contains no %s (%s)", command, what, wanted)
		}
	}
}

func pinnedLeaf(t *testing.T, hostname string) []byte {
	t.Helper()
	signer, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
	}
	raw, err := x509.CreateCertificate(rand.Reader, &template, &template, &signer.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})
}
