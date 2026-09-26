package host

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func TestAPinnedPairWhoseKeyIsNotTheCertificatesIsRefusedBeforeTheProxyLoadsIt(t *testing.T) {
	t.Parallel()

	pin := Pin{Hostname: "shop.example.com", Path: caddy.PinsDir + "/shop"}
	for verdict, refuses := range map[string]bool{pairMatched: false, pairMismatched: true, pairUnchecked: false, "": false} {
		leaf, _ := pinnedBlocks(t, []string{pin.Hostname}, 90*24*time.Hour)
		box := machine(nil)
		box.answer = func(command string) (session.Result, bool) {
			switch {
			case strings.Contains(command, "cat "+quoted(caddy.PinCertificate(pin.Path))):
				return session.Result{Stdout: string(leaf)}, true
			case strings.Contains(command, "openssl pkey -in "+quoted(caddy.PinKey(pin.Path))):
				return session.Result{Stdout: verdict + "\n"}, true
			}
			return session.Result{}, false
		}
		_, err := New(box.dial, Keys{}, []Pin{pin}, Front{}).VerifiedPins(context.Background())
		var refusal refusal.Refusal
		if refused := errors.As(err, &refusal); refused != refuses {
			t.Errorf("a pair the box reports %q vouches as %v, want refused=%v: a mismatched pair is one the proxy refuses at the flip, taking every hostname pinned to it off the air", verdict, err, refuses)
		}
		if refuses && (!strings.Contains(err.Error(), caddy.PinKey(pin.Path)) || !strings.Contains(err.Error(), caddy.PinCertificate(pin.Path))) {
			t.Errorf("the refusal reads %q and names neither the key nor the certificate", err)
		}
		for _, command := range box.commands() {
			if strings.Contains(command, "cat "+quoted(caddy.PinKey(pin.Path))) || (strings.Contains(command, caddy.PinKey(pin.Path)) && !strings.Contains(command, "-pubout")) {
				t.Errorf("checking the pair ran %q, which brings the private key across the session rather than a digest of its public half", command)
			}
		}
	}
}

func TestThePairCheckReadsOnlyPublicHalvesAndSaysWhenItCouldNotRun(t *testing.T) {
	t.Parallel()

	command := pairCommand(caddy.PinsDir + "/shop")
	for what, wanted := range map[string]string{
		"the certificate's public key":          "openssl x509 -in " + quoted(caddy.PinsDir+"/shop.crt") + " -noout -pubkey",
		"the key's public half":                 "openssl pkey -in " + quoted(caddy.PinsDir+"/shop.key") + " -pubout",
		"a verdict when openssl is absent":      "echo " + pairUnchecked,
		"a digest rather than the bytes":        "sha256sum",
		"one word for a pair that does not fit": "echo " + pairMismatched,
	} {
		if !strings.Contains(command, wanted) {
			t.Errorf("the pair check runs\n%s\nwhich contains no %s (%s)", command, what, wanted)
		}
	}
}
