package host

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const wildcard = "*.preview.example.com"

func pinnedBox(t *testing.T, pins []Pin, held map[string][]byte) *Host {
	t.Helper()

	stood := claimingBox(t, routed())
	serves := stood.answer
	stood.answer = func(command string) (session.Result, bool) {
		for path, block := range held {
			if strings.Contains(command, quoted(path)) {
				return session.Result{Stdout: string(block)}, true
			}
		}
		return serves(command)
	}
	return New(stood.dial, Keys{}, pins)
}

func claiming(t *testing.T, pins []Pin, held map[string][]byte) error {
	t.Helper()
	return pinnedBox(t, pins, held).ClaimHosts(context.Background(),
		[]HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}})
}

func TestAPinIsWrittenAtThePathTheProxyOpensAndReadBackAtThePathThisHostSpells(t *testing.T) {
	t.Parallel()

	at := ProxyPins + "/wildcard"
	state := routed()
	state.Pins = []Pin{{Hostname: wildcard, Path: at}}
	rendered, err := RenderProxyConfig(state)
	if err != nil {
		t.Fatalf("RenderProxyConfig() = %v", err)
	}
	if !strings.Contains(string(rendered), proxyPinsMount+"/wildcard"+pinCertificate) {
		t.Errorf("the config hands the proxy %q, and the proxy opens a pair at %s: a path this host spells is a path the container has no such file at, and the whole config is refused with it:\n%s",
			at, proxyPinsMount, rendered)
	}
	if strings.Contains(string(rendered), ProxyPins+"/wildcard") {
		t.Errorf("the config carries a path off this host's own filesystem:\n%s", rendered)
	}

	read, err := ReadProxyState(rendered)
	if err != nil {
		t.Fatalf("ReadProxyState() = %v", err)
	}
	if !slices.Equal(read.Pins, state.Pins) {
		t.Errorf("the pins read back as %v, want %v: what ocel reads a pinned pair off is the path on this host, and what it hands the proxy is the path inside it", read.Pins, state.Pins)
	}
}

func TestAPinTheProxyCouldNotOpenIsRefusedRatherThanRendered(t *testing.T) {
	t.Parallel()

	for what, pin := range map[string]Pin{
		"a pair beside ocel's own state": {Hostname: wildcard, Path: "/etc/ocel/wildcard"},
		"a pair the operator kept":       {Hostname: wildcard, Path: "/srv/certs/wildcard"},
		"a pair nested under the root":   {Hostname: wildcard, Path: ProxyPins + "/preview/wildcard"},
		"a pair reached by climbing out": {Hostname: wildcard, Path: ProxyPins + "/.."},
		"a pair naming no host":          {Hostname: "", Path: ProxyPins + "/wildcard"},
	} {
		state := routed()
		state.Pins = []Pin{pin}
		var refusal providerkit.Refusal
		if _, err := RenderProxyConfig(state); !errors.As(err, &refusal) {
			t.Errorf("RenderProxyConfig() over %s at %q = %v, want a refusal naming %s: anything the proxy cannot open takes every reshape on this box with it",
				what, pin.Path, err, ProxyPins)
		}
	}
}

func TestAPinIsVerifiedWhereItIsBoundRatherThanWhereItIsRead(t *testing.T) {
	t.Parallel()

	at := ProxyPins + "/wildcard"
	covering, _ := pinnedBlocks(t, []string{wildcard}, 90*24*time.Hour)
	elsewhere, _ := pinnedBlocks(t, []string{"*.other.example.com"}, 90*24*time.Hour)
	expired, _ := pinnedBlocks(t, []string{wildcard}, -time.Hour)

	for what, held := range map[string][]byte{
		"a pair covering something else":                        elsewhere,
		"a pair nothing on this box renews and nobody replaced": expired,
		"a file that is no certificate at all":                  []byte("-----BEGIN EC PRIVATE KEY-----\nMHcCAQE=\n-----END EC PRIVATE KEY-----\n"),
	} {
		err := claiming(t, []Pin{{Hostname: wildcard, Path: at}}, map[string][]byte{PinCertificate(at): held})
		var refusal providerkit.Refusal
		if !errors.As(err, &refusal) {
			t.Errorf("a claim on a box carrying %s = %v, want ocel's own refusal naming %s: the pin reaches the proxy on every reshape, so an unverified one turns a typo into a caddy error on somebody else's deploy",
				what, err, at)
		}
	}

	if err := claiming(t, []Pin{{Hostname: wildcard, Path: at}}, map[string][]byte{PinCertificate(at): covering}); err != nil {
		t.Errorf("a claim on a box carrying a pinned pair that covers what it is pinned for = %v, want the claim taken", err)
	}
}

func TestAPinIsReadOffTheBoxOnceRatherThanOnEveryReshape(t *testing.T) {
	t.Parallel()

	at := ProxyPins + "/wildcard"
	covering, _ := pinnedBlocks(t, []string{wildcard}, 90*24*time.Hour)
	stood := claimingBox(t, routed())
	serves := stood.answer
	reads := 0
	stood.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, quoted(PinCertificate(at))) {
			reads++
			return session.Result{Stdout: string(covering)}, true
		}
		return serves(command)
	}
	h := New(stood.dial, Keys{}, []Pin{{Hostname: wildcard, Path: at}})

	ctx := context.Background()
	for _, hostname := range []string{claimed, "blog.example.com", "www.example.com"} {
		if err := h.ClaimHosts(ctx, []HostClaim{{Hostname: hostname, Owner: surface, Pointer: pointed}}); err != nil {
			t.Fatalf("ClaimHosts(%s) = %v", hostname, err)
		}
	}
	if reads != 1 {
		t.Errorf("the box was asked for the pinned certificate %d times over three claims, want once: a pin is fixed for the life of this provider and every read of it is a round trip a deploy waits on", reads)
	}
}

func TestAPinTheProxyCouldNotOpenCoversNothingRatherThanNamingAHandle(t *testing.T) {
	t.Parallel()

	for what, pin := range map[string]Pin{
		"a pair the operator kept elsewhere": {Hostname: wildcard, Path: "/srv/certs/wildcard"},
		"a pair nested under the root":       {Hostname: wildcard, Path: ProxyPins + "/preview/wildcard"},
		"a pair reached by climbing out":     {Hostname: wildcard, Path: ProxyPins + "/.."},
	} {
		if at := Covering([]Pin{pin}, "shop.preview.example.com"); at != "" {
			t.Errorf("%s at %q covers the hostname as %q, and a status naming a handle no reshape will ever bind sends an operator looking for a pin that cannot exist",
				what, pin.Path, at)
		}
	}
	held := Pin{Hostname: wildcard, Path: ProxyPins + "/wildcard"}
	if at := Covering([]Pin{held}, "shop.preview.example.com"); at != held.Path {
		t.Errorf("Covering() over a pair the proxy loads = %q, want %q", at, held.Path)
	}
}

func TestOneCertificateCoveringTwoHostnamesIsHandedToTheProxyOnce(t *testing.T) {
	t.Parallel()

	at := ProxyPins + "/pair"
	state := routed()
	state.Pins = []Pin{{Hostname: "shop.example.com", Path: at}, {Hostname: "blog.example.com", Path: at}}
	rendered, err := RenderProxyConfig(state)
	if err != nil {
		t.Fatalf("RenderProxyConfig() = %v", err)
	}

	read, err := parsed(rendered)
	if err != nil {
		t.Fatal(err)
	}
	if read.Apps.TLS == nil {
		t.Fatal("the config declares no pinned pair at all")
	}
	files := read.Apps.TLS.Certificates.LoadFiles
	if len(files) != 1 {
		t.Fatalf("the proxy is handed %s %d times, want once: a pair loaded twice is one certificate cached under two tags, and which of them the proxy answers a handshake from is whichever load ran last",
			at, len(files))
	}
	if !slices.Equal(files[0].Tags, []string{"blog.example.com", "shop.example.com"}) {
		t.Errorf("the one entry is tagged %v, want every hostname the pair was pinned for: a tag dropped here is a hostname nothing on this box can say is pinned", files[0].Tags)
	}

	held, err := ReadProxyState(rendered)
	if err != nil {
		t.Fatalf("ReadProxyState() = %v", err)
	}
	want := []Pin{{Hostname: "blog.example.com", Path: at}, {Hostname: "shop.example.com", Path: at}}
	if !slices.Equal(held.Pins, want) {
		t.Errorf("the pins read back are %v, want %v: a deploy renders this file whole off what it reads, and a hostname lost in the round trip is a pair the next reshape stops loading", held.Pins, want)
	}
}
