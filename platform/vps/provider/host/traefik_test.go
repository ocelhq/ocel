package host

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy/traefik"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func traefikOnTheHost() Front {
	return Front{Traefik: &TraefikFront{
		Directory: "/etc/traefik/dynamic", Resolver: "letsencrypt", PreviewResolver: "cloudflare",
		Entrypoints: Entrypoints{HTTP: "web", HTTPS: "websecure"}, Port: 9000,
	}}
}

func flagged(command []string, flag string) []string {
	var values []string
	for at, arg := range command {
		if arg == flag && at+1 < len(command) {
			values = append(values, command[at+1])
		}
	}
	return values
}

func TestYourTraefikOpensAsTheTraefikProxyWithEverythingTheOptionFills(t *testing.T) {
	t.Parallel()

	opened, ok := openFront(coolifysTraefik(), frontBox{}).(traefik.Traefik)
	if !ok {
		t.Fatalf("Coolify's Traefik opens as %T, want the traefik proxy", openFront(coolifysTraefik(), frontBox{}))
	}
	want := traefik.Traefik{Box: frontBox{}, Directory: "/data/coolify/proxy/dynamic", Resolver: "letsencrypt", HTTP: "http", HTTPS: "https", Network: "coolify"}
	if opened != want {
		t.Errorf("Coolify's Traefik opens as %+v, want %+v", opened, want)
	}
	if file := opened.File(); file != "/data/coolify/proxy/dynamic/ocel.yml" {
		t.Errorf("File() = %q, want ocel.yml in Coolify's dynamic directory", file)
	}
}

func TestTheSwitchboardBesideYourTraefikOnItsNetworkHearsHTTPSThereAloneAndStartsOnIt(t *testing.T) {
	t.Parallel()

	board := switchboardOf(t, coolifysTraefik())
	if got := flagged(board.command, "--https-listen"); !slices.Equal(got, []string{"coolify:" + switchboard.HTTPSListenPort}) {
		t.Errorf("the switchboard serves as %q, want https heard on its own address on coolify alone", board.command)
	}
	if got := flagged(board.command, "--relay-network"); len(got) != 0 {
		t.Errorf("the switchboard serves as %q, want no network relayed from: every tenant on coolify would name its own scheme and host", board.command)
	}
	running := words(board.run())
	if !strings.Contains(running, words([]string{"--network", ProxyNetwork, "--network", "coolify"})) {
		t.Errorf("the switchboard runs as %s, want it started on coolify: the https listener binds its address there once, as it starts", running)
	}
	if strings.Contains(running, "--publish") {
		t.Errorf("the switchboard runs as %s, want nothing published: your Traefik reaches it on coolify", running)
	}
	if !slices.Contains(board.binds, "/data/coolify/proxy/dynamic:/data/coolify/proxy/dynamic") {
		t.Errorf("the switchboard binds %q, want Coolify's dynamic directory to place ocel.yml in", board.binds)
	}
	if written := board.writing(containerRising); !strings.Contains(written, "proxy.traefik.network") {
		t.Errorf("the switchboard write refuses a missing network without naming the option that set it:\n%s", written)
	}
}

func TestTheSwitchboardBesideATraefikOnTheHostHearsHTTPSOnItsBoxNetworkAddressPublishedOnLoopback(t *testing.T) {
	t.Parallel()

	board := switchboardOf(t, traefikOnTheHost())
	if got := flagged(board.command, "--https-listen"); !slices.Equal(got, []string{ProxyNetwork + ":" + switchboard.HTTPSListenPort}) {
		t.Errorf("the switchboard serves as %q, want https heard on its own address on %s, where the loopback port is published to", board.command, ProxyNetwork)
	}
	if got := flagged(board.command, "--relay-network"); len(got) != 0 {
		t.Errorf("the switchboard serves as %q, want no network relayed from", board.command)
	}
	if running := words(board.run()); !strings.Contains(running, words([]string{"--publish", "127.0.0.1:9000:" + switchboard.HTTPSListenPort})) {
		t.Errorf("the switchboard runs as %s, want the https listener published on 127.0.0.1:9000", running)
	}
}

func TestASwitchboardADeployStandsAgainBesideYourTraefikHearsHTTPSWhereBootstrapHadIt(t *testing.T) {
	t.Parallel()

	restored := switchboardBox(nil, coolifysTraefik()).restoring(containerRising)
	for _, want := range []string{
		words([]string{"--https-listen", "coolify:" + switchboard.HTTPSListenPort}),
		words([]string{"--network", ProxyNetwork, "--network", "coolify"}),
	} {
		if !strings.Contains(restored, want) {
			t.Errorf("a deploy stands the switchboard again as\n%s\nwithout %s", restored, want)
		}
	}
}

func TestYourTraefiksRoutersAreReadAsRootByALoginOutsideTheDockerGroup(t *testing.T) {
	rig := machine(nil)
	rig.facts = session.Facts{Systemd: true}
	var unelevated []string
	rig.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker") && !strings.HasPrefix(command, "sudo -n ") {
			unelevated = append(unelevated, command)
			return session.Result{Code: 1, Stderr: "permission denied while trying to connect to the Docker daemon socket"}, true
		}
		if strings.Contains(command, "beside") {
			return session.Result{Stdout: "[]"}, true
		}
		return session.Result{}, false
	}

	front := openFront(traefikOnTheHost(), frontBox{rig.host()})
	if err := front.RefuseRouted(context.Background(), []string{"shop.example.com"}); err != nil {
		t.Errorf("RefuseRouted() as a login outside the docker group = %v, want the labels read as root", err)
	}
	if len(unelevated) > 1 {
		t.Errorf("the router scan ran %q without sudo, want only the probe that finds the socket denied", unelevated)
	}
}
