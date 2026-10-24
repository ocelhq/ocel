package host

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const wildcard = "*.preview.example.com"

func pinnedBox(t *testing.T, pins []boxapi.Pin, files map[string][]byte) *Host {
	t.Helper()

	_, h := projectBox(t, routed(), "shop", pins, files)
	return h
}

func projectBox(t *testing.T, state boxapi.RoutingTable, project string, pins []boxapi.Pin, files map[string][]byte) (*claimBench, *Host) {
	t.Helper()

	rig := claimingBox(t, state)
	serves := rig.answer
	rig.answer = func(command string) (ssh.Result, bool) {
		if pinned, said := answeringPins(command, files); said {
			return pinned, true
		}
		return serves(command)
	}
	return rig, New(ssh.NewBox(rig.dial, nil), project, Keys{}, pins, front.Proxy{})
}

func recordedPins(t *testing.T, rig *claimBench) []boxapi.Pin {
	t.Helper()

	table, err := routing.ReadRoutingTable([]byte(rig.recorded))
	if err != nil {
		t.Fatalf("ReadRoutingTable() = %v", err)
	}
	return table.Pins
}

func coveringPin(t *testing.T, hostname, at string) ([]boxapi.Pin, map[string][]byte) {
	t.Helper()

	covering, _ := pinnedBlocks(t, []string{hostname}, 90*24*time.Hour)
	return []boxapi.Pin{{Hostname: hostname, Path: at}}, map[string][]byte{at: covering}
}

func pinHelper(at string) string {
	leaf, _ := boxapi.Pinned(at)
	return shell.Words([]string{boxapi.LiveBinary, boxapi.PinCommand, leaf})
}

func answeringPins(command string, stored map[string][]byte) (ssh.Result, bool) {
	for at, block := range stored {
		if strings.HasSuffix(command, pinHelper(at)) {
			said, err := json.Marshal(boxapi.PinnedPair{Certificate: string(block), KeyMatches: true})
			if err != nil {
				panic(err)
			}
			return ssh.Result{Stdout: string(said) + "\n"}, true
		}
	}
	return ssh.Result{}, false
}

func pinning(t *testing.T) (*claimBench, *Host) {
	t.Helper()

	at := boxapi.PinsDir + "/wildcard"
	covering, _ := pinnedBlocks(t, []string{wildcard}, 90*24*time.Hour)
	rig := claimingBox(t, routed())
	serves := rig.answer
	rig.answer = func(command string) (ssh.Result, bool) {
		if pinned, said := answeringPins(command, map[string][]byte{at: covering}); said {
			return pinned, true
		}
		return serves(command)
	}
	return rig, New(ssh.NewBox(rig.dial, nil), "shop", Keys{}, []boxapi.Pin{{Hostname: wildcard, Path: at}}, front.Proxy{})
}

func claiming(t *testing.T, pins []boxapi.Pin, files map[string][]byte) error {
	t.Helper()
	return pinnedBox(t, pins, files).routing.ClaimHosts(context.Background(),
		[]boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}})
}

func TestAPinIsWrittenAtThePathTheProxyOpensAndReadBackAtThePathThisHostSpells(t *testing.T) {
	t.Parallel()

	at := boxapi.PinsDir + "/wildcard"
	state := routed()
	state.Claims = []boxapi.Claim{{Hostname: "shop.preview.example.com", Owner: surface, Pointer: pointed}}
	state.Pins = []boxapi.Pin{{Hostname: wildcard, Path: at}}
	rendered, err := routing.RenderProxyConfig(caddy.Builtin{}, state)
	if err != nil {
		t.Fatalf("RenderProxyConfig(caddy.Builtin{}, ) = %v", err)
	}
	if !strings.Contains(string(rendered), boxapi.PinCertificate(caddy.PinsMount+"/wildcard")) {
		t.Errorf("the config hands the proxy %q, and the proxy opens a pair at %s: a path this host spells is a path the container has no such file at, and the whole config is refused with it:\n%s",
			at, caddy.PinsMount, rendered)
	}
	if strings.Contains(string(rendered), boxapi.PinsDir+"/wildcard") {
		t.Errorf("the config names a path off this host's own filesystem:\n%s", rendered)
	}

	read, err := routing.ReadRoutingTable(mustWrite(t, state))
	if err != nil {
		t.Fatalf("ReadRoutingTable() = %v", err)
	}
	if !slices.Equal(read.Pins, state.Pins) {
		t.Errorf("the pins read back as %v, want %v: what ocel reads a pinned pair off is the path on this host, and what it hands the proxy is the path inside it", read.Pins, state.Pins)
	}
}

func TestAPinTheProxyCouldNotOpenIsRefusedRatherThanRendered(t *testing.T) {
	t.Parallel()

	for what, pin := range map[string]boxapi.Pin{
		"a pair beside ocel's own state": {Hostname: wildcard, Path: "/etc/ocel/wildcard"},
		"a pair the operator kept":       {Hostname: wildcard, Path: "/srv/certs/wildcard"},
		"a pair nested under the root":   {Hostname: wildcard, Path: boxapi.PinsDir + "/preview/wildcard"},
		"a pair reached by climbing out": {Hostname: wildcard, Path: boxapi.PinsDir + "/.."},
		"a pair naming no host":          {Hostname: "", Path: boxapi.PinsDir + "/wildcard"},
	} {
		state := routed()
		state.Pins = []boxapi.Pin{pin}
		var refused refusal.Refusal
		if _, err := routing.RenderProxyConfig(caddy.Builtin{}, state); !errors.As(err, &refused) {
			t.Errorf("RenderProxyConfig(caddy.Builtin{}, ) over %s at %q = %v, want a refusal naming %s: anything the proxy cannot open takes every reshape on this box with it",
				what, pin.Path, err, boxapi.PinsDir)
		}
	}
}

func TestAPinIsVerifiedWhereItIsBoundRatherThanWhereItIsRead(t *testing.T) {
	t.Parallel()

	at := boxapi.PinsDir + "/wildcard"
	covering, _ := pinnedBlocks(t, []string{wildcard}, 90*24*time.Hour)
	elsewhere, _ := pinnedBlocks(t, []string{"*.other.example.com"}, 90*24*time.Hour)
	expired, _ := pinnedBlocks(t, []string{wildcard}, -time.Hour)

	for what, stored := range map[string][]byte{
		"a pair covering something else":                        elsewhere,
		"a pair nothing on this box renews and nobody replaced": expired,
		"a file that is no certificate at all":                  []byte("-----BEGIN EC PRIVATE KEY-----\nMHcCAQE=\n-----END EC PRIVATE KEY-----\n"),
	} {
		err := claiming(t, []boxapi.Pin{{Hostname: wildcard, Path: at}}, map[string][]byte{at: stored})
		var refused refusal.Refusal
		if !errors.As(err, &refused) {
			t.Errorf("a claim on a box storing %s = %v, want ocel's own refusal naming %s: the pin reaches the proxy on every reshape, so an unverified one turns a typo into a caddy error on somebody else's deploy",
				what, err, at)
		}
	}

	if err := claiming(t, []boxapi.Pin{{Hostname: wildcard, Path: at}}, map[string][]byte{at: covering}); err != nil {
		t.Errorf("a claim on a box storing a pinned pair that covers what it is pinned for = %v, want the claim taken", err)
	}
}

func TestAPinIsReadOffTheBoxOnceRatherThanOnEveryReshape(t *testing.T) {
	t.Parallel()

	at := boxapi.PinsDir + "/wildcard"
	covering, _ := pinnedBlocks(t, []string{wildcard}, 90*24*time.Hour)
	rig := claimingBox(t, routed())
	serves := rig.answer
	reads := 0
	rig.answer = func(command string) (ssh.Result, bool) {
		if pinned, said := answeringPins(command, map[string][]byte{at: covering}); said {
			reads++
			return pinned, true
		}
		return serves(command)
	}
	h := New(ssh.NewBox(rig.dial, nil), "shop", Keys{}, []boxapi.Pin{{Hostname: wildcard, Path: at}}, front.Proxy{})

	ctx := context.Background()
	for _, hostname := range []string{claimed, "blog.example.com", "www.example.com"} {
		if err := h.routing.ClaimHosts(ctx, []boxapi.Claim{{Hostname: hostname, Owner: surface, Pointer: pointed}}); err != nil {
			t.Fatalf("ClaimHosts(%s) = %v", hostname, err)
		}
	}
	if reads != 1 {
		t.Errorf("the box was asked for the pinned certificate %d times over three claims, want once: a pin is fixed for the life of this provider and every read of it is a round trip a deploy waits on", reads)
	}
}

func TestAPinTheProxyCouldNotOpenCoversNothingRatherThanNamingAHandle(t *testing.T) {
	t.Parallel()

	for what, pin := range map[string]boxapi.Pin{
		"a pair the operator kept elsewhere": {Hostname: wildcard, Path: "/srv/certs/wildcard"},
		"a pair nested under the root":       {Hostname: wildcard, Path: boxapi.PinsDir + "/preview/wildcard"},
		"a pair reached by climbing out":     {Hostname: wildcard, Path: boxapi.PinsDir + "/.."},
	} {
		if at := certs.Covering([]boxapi.Pin{pin}, "shop.preview.example.com"); at != "" {
			t.Errorf("%s at %q covers the hostname as %q, and a status naming a handle no reshape will ever bind sends an operator looking for a pin that cannot exist",
				what, pin.Path, at)
		}
	}
	loaded := boxapi.Pin{Hostname: wildcard, Path: boxapi.PinsDir + "/wildcard"}
	if at := certs.Covering([]boxapi.Pin{loaded}, "shop.preview.example.com"); at != loaded.Path {
		t.Errorf("Covering() over a pair the proxy loads = %q, want %q", at, loaded.Path)
	}
}

func TestOneCertificateCoveringTwoHostnamesIsHandedToTheProxyOnce(t *testing.T) {
	t.Parallel()

	at := boxapi.PinsDir + "/pair"
	state := routed()
	state.Claims = []boxapi.Claim{{Hostname: "shop.example.com", Owner: surface, Pointer: pointed}, {Hostname: "blog.example.com", Owner: surface, Pointer: pointed}}
	state.Pins = []boxapi.Pin{{Hostname: "shop.example.com", Path: at}, {Hostname: "blog.example.com", Path: at}}
	rendered, err := routing.RenderProxyConfig(caddy.Builtin{}, state)
	if err != nil {
		t.Fatalf("RenderProxyConfig(caddy.Builtin{}, ) = %v", err)
	}

	var read struct {
		Apps struct {
			TLS *struct {
				Certificates struct {
					LoadFiles []struct {
						Tags []string `json:"tags"`
					} `json:"load_files"`
				} `json:"certificates"`
			} `json:"tls"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(rendered, &read); err != nil {
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
	if want := []string{caddy.PinsMount + "/pair"}; !slices.Equal(files[0].Tags, want) {
		t.Errorf("the one entry is tagged %v, want %v: the pair's own path is what a handshake for either hostname selects it by, and a tag naming the hostnames it covers changes the proxy's config with every bind under it", files[0].Tags, want)
	}

	table, err := routing.ReadRoutingTable(mustWrite(t, state))
	if err != nil {
		t.Fatalf("ReadRoutingTable() = %v", err)
	}
	want := []boxapi.Pin{{Hostname: "blog.example.com", Path: at}, {Hostname: "shop.example.com", Path: at}}
	if !slices.Equal(table.Pins, want) {
		t.Errorf("the pins read back are %v, want %v: a deploy renders this file whole off what it reads, and a hostname lost in the round trip is a pair the next reshape stops loading", table.Pins, want)
	}
}

type loadedPair struct {
	Certificate string `json:"certificate"`
}

func TestAPinnedPreviewWildcardServesTheProbeBeforeAnyPreviewIsClaimed(t *testing.T) {
	t.Parallel()

	state := previewing()
	state.Pins = []boxapi.Pin{{Hostname: wildcard, Path: boxapi.PinsDir + "/wildcard"}}
	var read struct {
		Apps struct {
			TLS *struct {
				Certificates struct {
					LoadFiles []loadedPair `json:"load_files"`
				} `json:"certificates"`
			} `json:"tls"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(mustRender(t, state), &read); err != nil {
		t.Fatal(err)
	}
	var loaded []loadedPair
	if read.Apps.TLS != nil {
		loaded = read.Apps.TLS.Certificates.LoadFiles
	}
	if !slices.ContainsFunc(loaded, func(pair loadedPair) bool {
		return pair.Certificate == boxapi.PinCertificate(caddy.PinsMount+"/wildcard")
	}) {
		t.Errorf("a box answering previews under a pinned %s hands the proxy %+v, want that pair loaded: the probe %s is answered whether or not a preview is claimed, and with no pair loaded for it the proxy orders one from acme, which a dns-01-only setup never gets", wildcard, loaded, edge.ProbeHostname(wildcard))
	}
}

func TestAReshapeByOneProjectKeepsThePinsAnotherProjectBound(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	state.Pins = []boxapi.Pin{{Project: "shop", Hostname: claimed, Path: boxapi.PinsDir + "/shop"}}
	rig, blog := projectBox(t, state, "blog", nil, nil)
	if err := blog.routing.ClaimHosts(context.Background(), []boxapi.Claim{
		{Hostname: "blog.example.com", Owner: boxapi.Surface("blog", "production"), Pointer: pointed},
	}); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	if held := recordedPins(t, rig); !slices.Equal(held, state.Pins) {
		t.Errorf("after blog claimed a hostname the box pins %v, want shop's %v kept: blog's options speak only for blog", held, state.Pins)
	}
}

func TestAReshapeReplacesOnlyThePinsOfTheProjectReshaping(t *testing.T) {
	t.Parallel()

	shop := boxapi.Pin{Project: "shop", Hostname: claimed, Path: boxapi.PinsDir + "/shop"}
	state := routed()
	state.Pins = []boxapi.Pin{shop, {Project: "blog", Hostname: "blog.example.com", Path: boxapi.PinsDir + "/blog-old"}}
	at := boxapi.PinsDir + "/blog"
	pins, files := coveringPin(t, "blog.example.com", at)
	rig, blog := projectBox(t, state, "blog", pins, files)
	if err := blog.routing.ClaimHosts(context.Background(), []boxapi.Claim{
		{Hostname: "blog.example.com", Owner: boxapi.Surface("blog", "production"), Pointer: pointed},
	}); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	want := []boxapi.Pin{{Project: "blog", Hostname: "blog.example.com", Path: at}, shop}
	if held := recordedPins(t, rig); !slices.Equal(held, want) {
		t.Errorf("after blog reshaped with a new pin the box pins %v, want %v: blog's pins replace blog's own and leave shop's", held, want)
	}
}

func TestAPinRecordedWithoutAProjectIsKeptByEveryProjectsReshape(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Pins = []boxapi.Pin{{Hostname: claimed, Path: boxapi.PinsDir + "/shop"}}
	rig, blog := projectBox(t, state, "blog", nil, nil)
	if err := blog.routing.ClaimHosts(context.Background(), []boxapi.Claim{
		{Hostname: "blog.example.com", Owner: boxapi.Surface("blog", "production"), Pointer: pointed},
	}); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	if held := recordedPins(t, rig); !slices.Equal(held, state.Pins) {
		t.Errorf("a pin recorded with no project reads back after blog's reshape as %v, want %v kept: no project speaks for it, so none may drop it", held, state.Pins)
	}
}

func TestUnpinningAProjectThatHoldsNothingOnTheBoxDropsOnlyItsPins(t *testing.T) {
	t.Parallel()

	at := boxapi.PinsDir + "/shop"
	pins, files := coveringPin(t, claimed, at)
	blog := boxapi.Pin{Project: "blog", Hostname: "blog.example.com", Path: boxapi.PinsDir + "/blog"}
	state := routed()
	state.Claims = []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	state.Pins = []boxapi.Pin{blog, {Project: "shop", Hostname: claimed, Path: at}}
	rig, shop := projectBox(t, state, "shop", pins, files)
	ctx := context.Background()
	if err := shop.routing.DisclaimSurface(ctx, surface); err != nil {
		t.Fatalf("DisclaimSurface() = %v", err)
	}
	if err := shop.routing.UnrouteSurface(ctx, surface); err != nil {
		t.Fatalf("UnrouteSurface() = %v", err)
	}
	if err := shop.routing.Unpin(ctx, "shop"); err != nil {
		t.Fatalf("Unpin() = %v", err)
	}
	if held := recordedPins(t, rig); !slices.Equal(held, []boxapi.Pin{blog}) {
		t.Errorf("after shop's last surface left the box it pins %v, want only blog's %v: a removed project's pins load a certificate for hostnames nothing on the box claims any more", held, blog)
	}
}

func TestUnpinningAProjectWhoseOtherTierStillHoldsTheBoxKeepsItsPins(t *testing.T) {
	t.Parallel()

	at := boxapi.PinsDir + "/shop"
	pins, files := coveringPin(t, claimed, at)
	state := routed()
	state.Claims = []boxapi.Claim{
		{Hostname: claimed, Owner: surface, Pointer: pointed},
		{Hostname: "pr-1.preview.example.com", Owner: boxapi.Surface("shop", "preview"), Pointer: "@pr-1"},
	}
	state.Pins = []boxapi.Pin{{Project: "shop", Hostname: claimed, Path: at}}
	rig, shop := projectBox(t, state, "shop", pins, files)
	ctx := context.Background()
	if err := shop.routing.DisclaimSurface(ctx, surface); err != nil {
		t.Fatalf("DisclaimSurface() = %v", err)
	}
	if err := shop.routing.Unpin(ctx, "shop"); err != nil {
		t.Fatalf("Unpin() = %v", err)
	}
	if held := recordedPins(t, rig); !slices.Equal(held, state.Pins) {
		t.Errorf("after shop's production surface left the box it pins %v, want %v kept: shop's preview surface still claims a hostname on it", held, state.Pins)
	}
}

func previewEntryShared(t *testing.T) (*claimBench, *Host, *Host) {
	t.Helper()

	rig, shop := pinning(t)
	ctx := context.Background()
	if err := shop.routing.InstallPreviewEntry(ctx, previewBase); err != nil {
		t.Fatalf("InstallPreviewEntry() = %v", err)
	}
	blog := New(ssh.NewBox(rig.dial, nil), "blog", Keys{}, nil, front.Proxy{})
	if err := blog.routing.ClaimHosts(ctx, []boxapi.Claim{
		{Hostname: "pr-1." + previewBase, Owner: boxapi.Surface("blog", "preview"), Pointer: "@pr-1"},
	}); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	return rig, shop, blog
}

func destroyed(t *testing.T, h *Host, project string) {
	t.Helper()

	ctx := context.Background()
	for _, owner := range []string{boxapi.Surface(project, "production"), boxapi.Surface(project, "preview")} {
		if err := h.routing.DisclaimSurface(ctx, owner); err != nil {
			t.Fatalf("DisclaimSurface(%s) = %v", owner, err)
		}
		if err := h.routing.UnrouteSurface(ctx, owner); err != nil {
			t.Fatalf("UnrouteSurface(%s) = %v", owner, err)
		}
	}
	if err := h.routing.Unpin(ctx, project); err != nil {
		t.Fatalf("Unpin() = %v", err)
	}
}

func TestUnpinningTheProjectThatInstalledThePreviewEntryKeepsTheWildcardWhileTheEntryAnswers(t *testing.T) {
	t.Parallel()

	rig, shop, _ := previewEntryShared(t)
	destroyed(t, shop, "shop")
	table, err := routing.ReadRoutingTable([]byte(rig.recorded))
	if err != nil {
		t.Fatalf("ReadRoutingTable() = %v", err)
	}
	want := []boxapi.Pin{{Project: "shop", Hostname: wildcard, Path: boxapi.PinsDir + "/wildcard"}}
	if !slices.Equal(table.Pins, want) {
		t.Errorf("after shop left a box still answering previews on %s it pins %v, want %v: the entry is the box's, and its probe and blog's preview fall back to acme without the pair", wildcard, table.Pins, want)
	}
	for _, hostname := range []string{edge.ProbeHostname(wildcard), "pr-1." + previewBase} {
		if admittedBy(t, table)(hostname) {
			t.Errorf("the switchboard admits %s for a certificate the proxy orders from acme, want it served off the pinned %s", hostname, wildcard)
		}
	}
}

func TestRemovingThePreviewEntryDropsTheWildcardPinOfAProjectThatHoldsNothingElse(t *testing.T) {
	t.Parallel()

	rig, shop, blog := previewEntryShared(t)
	destroyed(t, shop, "shop")
	destroyed(t, blog, "blog")
	if err := blog.routing.RemovePreviewEntry(context.Background(), previewBase); err != nil {
		t.Fatalf("RemovePreviewEntry() = %v", err)
	}
	if held := recordedPins(t, rig); len(held) != 0 {
		t.Errorf("after the preview entry shop's pin was kept for went, the box pins %v, want none: nothing on the box is shop's any more", held)
	}
}

func TestRemovingThePreviewEntryKeepsTheWildcardPinOfAProjectStillOnTheBox(t *testing.T) {
	t.Parallel()

	rig, _, blog := previewEntryShared(t)
	destroyed(t, blog, "blog")
	if err := blog.routing.RemovePreviewEntry(context.Background(), previewBase); err != nil {
		t.Fatalf("RemovePreviewEntry() = %v", err)
	}
	want := []boxapi.Pin{{Project: "shop", Hostname: wildcard, Path: boxapi.PinsDir + "/wildcard"}}
	if held := recordedPins(t, rig); !slices.Equal(held, want) {
		t.Errorf("after blog removed the preview entry the box pins %v, want shop's %v: shop still routes on the box and its options still pin the pair", held, want)
	}
}
