package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh/sshtest"
)

func pinHelper(pin boxapi.Pin) string {
	leaf, _ := boxapi.Pinned(pin.Path)
	return shell.Words([]string{boxapi.LiveBinary, boxapi.PinCommand, leaf})
}

func answeringPin(t *testing.T, pin boxapi.Pin, pair boxapi.PinnedPair) *sshtest.Box {
	t.Helper()
	said, err := json.Marshal(pair)
	if err != nil {
		t.Fatal(err)
	}
	return sshtest.New(func(command string) (ssh.Result, bool) {
		if strings.HasSuffix(command, pinHelper(pin)) {
			return ssh.Result{Stdout: string(said) + "\n"}, true
		}
		return ssh.Result{}, false
	})
}

func TestTheDeployLoginVerifiesAPinThroughThePinHelperAndNeverReadsTheKey(t *testing.T) {
	t.Parallel()

	pin := boxapi.Pin{Hostname: "shop.example.com", Path: boxapi.PinsDir + "/shop"}
	leaf := pinnedLeaf(t, pin.Hostname)
	box := answeringPin(t, pin, boxapi.PinnedPair{Certificate: string(leaf), KeyMatches: true})
	box.Facts, box.User = ssh.Facts{Systemd: true}, boxapi.DeployLogin
	pins := NewPins(box.Open(), []boxapi.Pin{pin})

	if _, err := pins.Verified(context.Background()); err != nil {
		t.Fatalf("verifying a matched pin as %s, which has no sudo but its own lines: %v", boxapi.DeployLogin, err)
	}
	read, err := pins.Certificate(context.Background(), pin.Path)
	if err != nil || string(read) != string(leaf) {
		t.Fatalf("reading the pinned certificate as %s = %q, %v, want the certificate the helper printed", boxapi.DeployLogin, read, err)
	}
	for _, command := range box.Commands() {
		if strings.Contains(command, "id -u") {
			continue
		}
		if command != "sudo -n "+pinHelper(pin) {
			t.Errorf("reading the pin ran %q, want only the pin helper through the deploy login's sudoers line", command)
		}
		if strings.Contains(command, boxapi.PinKey(pin.Path)) || strings.Contains(command, "cat ") {
			t.Errorf("reading the pin ran %q, which brings the private key across the session", command)
		}
	}
}

func TestAPinnedPairWhoseKeyIsNotTheCertificatesIsRefusedBeforeTheProxyLoadsIt(t *testing.T) {
	t.Parallel()

	pin := boxapi.Pin{Hostname: "shop.example.com", Path: boxapi.PinsDir + "/shop"}
	for _, matches := range []bool{true, false} {
		box := answeringPin(t, pin, boxapi.PinnedPair{Certificate: string(pinnedLeaf(t, pin.Hostname)), KeyMatches: matches})
		_, err := NewPins(box.Open(), []boxapi.Pin{pin}).Verified(context.Background())
		var refused refusal.Refusal
		if refused := errors.As(err, &refused); refused == matches {
			t.Errorf("a pair whose key matches = %v vouches as %v: a mismatched pair is one the proxy refuses at the cutover, taking every hostname pinned to it off the air", matches, err)
		}
		if !matches && (!strings.Contains(err.Error(), boxapi.PinKey(pin.Path)) || !strings.Contains(err.Error(), boxapi.PinCertificate(pin.Path))) {
			t.Errorf("the refusal reads %q and names neither the key nor the certificate", err)
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

func TestAPinReadHandsThePinHelperItsRunBoxCommandSpanAndSocketWithinTheSudoersLine(t *testing.T) {
	ctx, spans := tracingtest.Record(t)
	pin := boxapi.Pin{Hostname: "shop.example.com", Path: boxapi.PinsDir + "/shop"}
	leaf := pinnedLeaf(t, pin.Hostname)
	said, err := json.Marshal(boxapi.PinnedPair{Certificate: string(leaf), KeyMatches: true})
	if err != nil {
		t.Fatal(err)
	}
	sudoers := "sudo -n " + shell.Words([]string{boxapi.LiveBinary, boxapi.PinCommand}) + " "
	box := sshtest.New(func(command string) (ssh.Result, bool) {
		if strings.HasPrefix(command, sudoers) && strings.HasSuffix(command, " 'shop'") {
			return ssh.Result{Stdout: string(said) + "\n"}, true
		}
		return ssh.Result{}, false
	})
	box.Facts, box.User, box.SpanSocket = ssh.Facts{Systemd: true}, boxapi.DeployLogin, "/tmp/ocel-spans.abc/otlp.sock"

	opened := box.Open()
	opened.SendTraceFlagsWhen(func(context.Context) (bool, error) { return true, nil })
	if _, err := NewPins(opened, []boxapi.Pin{pin}).Certificate(ctx, pin.Path); err != nil {
		t.Fatalf("Certificate() = %v", err)
	}
	parent := tracingtest.SpanNamed(t, spans.Ended(), "run box command").SpanContext()
	want := sudoers + shell.Words([]string{
		"--traceparent=00-" + parent.TraceID().String() + "-" + parent.SpanID().String() + "-01",
		"--trace-socket=/tmp/ocel-spans.abc/otlp.sock",
		"shop",
	})
	if ran := box.Commands(); ran[len(ran)-1] != want {
		t.Errorf("the pin helper ran as %q, want %q: the trace flags after `pin`, where the sudoers line matches anything", ran[len(ran)-1], want)
	}
}

func TestAPinReadOnABoxWhoseOcelLivePredatesTraceFlagsRunsUntraced(t *testing.T) {
	ctx, _ := tracingtest.Record(t)
	pin := boxapi.Pin{Hostname: "shop.example.com", Path: boxapi.PinsDir + "/shop"}
	said, err := json.Marshal(boxapi.PinnedPair{Certificate: string(pinnedLeaf(t, pin.Hostname)), KeyMatches: true})
	if err != nil {
		t.Fatal(err)
	}
	pinning := "sudo -n " + shell.Words([]string{boxapi.LiveBinary, boxapi.PinCommand}) + " "
	box := sshtest.New(func(command string) (ssh.Result, bool) {
		if !strings.HasPrefix(command, pinning) {
			return ssh.Result{}, false
		}
		if command != pinning+"'shop'" {
			return ssh.Result{Code: 1, Stderr: "usage: ocel-live pin <name>"}, true
		}
		return ssh.Result{Stdout: string(said) + "\n"}, true
	})
	box.Facts, box.User, box.SpanSocket = ssh.Facts{Systemd: true}, boxapi.DeployLogin, "/tmp/ocel-spans.abc/otlp.sock"
	opened := box.Open()
	opened.SendTraceFlagsWhen(func(context.Context) (bool, error) { return false, nil })

	if _, err := NewPins(opened, []boxapi.Pin{pin}).Certificate(ctx, pin.Path); err != nil {
		t.Fatalf("Certificate() = %v, want a box bootstrapped before trace flags to read its pin as it did", err)
	}
}
